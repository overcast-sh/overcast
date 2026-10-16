package ec2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// This file holds the low-level Docker-network primitives used by the VPC
// network strategies in vpc_strategy.go. All policy decisions (when to
// create, share, or skip a network) live in the strategies — this file only
// knows how to talk to Docker.

// reconcileNetworks is the entrypoint wired from router.reconcileDockerNetworks
// via Service.ReconcileNetworks. It delegates to the active strategy and must
// tolerate every error path without aborting overcastd startup.
//
// Every region the store holds is reconciled, not the one the context
// resolves to. The store keys VPCs by region and this runs with no request
// context, so a list made through it covers the default region alone — and
// did: a VPC persisted in ap-southeast-2 came back from a restart with no
// network and absent from health, while the default region's pass, seeing a
// network that no VPC in *its* view named, removed the one it still had. The
// Docker snapshot is one index shared across the regions (vpcNetworkIndex), so
// each region claims its own and the orphan sweep runs once, over what none
// of them claimed.
func (h *Handler) reconcileNetworks(ctx context.Context, snapshot []docker.NetworkSummary) {
	log := h.log.WithRecorder(ctx)

	if !h.dockerReady.Load() {
		return
	}
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	// Whatever an earlier pass established is void: this one runs because
	// Docker (re)appeared, and what the daemon has now is the question.
	h.reconciledAll.Store(false)
	h.reconciledRegions.Clear()

	// A store still completing its startup migration lists no VPCs however
	// many it holds, and this pass reads the store to decide what the daemon's
	// networks mean. Every one of them then looks unclaimed — by no VPC in any
	// region — and the orphan sweep at the end *removes* them: the pass does
	// not merely miss the chance to heal a record, it deletes the network the
	// record names. What the seed then loads is a VPC pointing at a network
	// that no longer exists, and every ECS task and Lambda placed in it fails
	// with "network … not found" until the next restart.
	//
	// So nothing is done at all, and reconciledAll is left clear: the
	// per-region backstop (ensureRegionReconciled) then covers each region on
	// the first placement into it, against a store that has since finished.
	// The router waits for the store before calling this at all
	// (router.awaitStoreReady), so this is the path a wait that expired takes.
	if h.store.notReady() {
		log.Warn("reconcile networks: the state store is still migrating — skipping this pass; regions will be reconciled on first use")
		return
	}

	byRegion, aerr := h.store.vpcsByRegion(ctx)
	if aerr != nil {
		log.Error("reconcile networks: list VPCs", zap.String("error", aerr.Message))
		return
	}
	regions := make([]string, 0, len(byRegion))
	for region := range byRegion {
		regions = append(regions, region)
	}
	sort.Strings(regions)

	// The snapshot was taken before the scan above, and nothing serialises
	// CreateVpc against this pass: it creates the network first and records
	// it after. Adoption and the sweep therefore work from different lists.
	//
	// Adoption works from a list taken after the scan, so that every record
	// seen has a network older than the list — the ordering
	// ensureRegionReconciled keeps for the same reason. Against the snapshot
	// a VPC created in between is a record naming a network the index lacks:
	// nothing adopts it, its name is refused on create, and it is written
	// unbacked with reconciledAll then vouching for it until Docker next
	// reappears.
	//
	// The sweep keeps to the snapshot. A network in the later list only is
	// newer than the scan, and its record may be on its way; swept, it would
	// leave that record naming a network that is gone, which is worse than
	// unbacked. The snapshot serves both when the daemon cannot be listed now.
	listed := snapshot
	if h.docker != nil {
		if fresh, err := h.docker.ListNetworks(ctx, serviceName); err == nil {
			listed = fresh
		} else {
			log.Warn("reconcile networks: list networks after the store scan; using the snapshot taken before it",
				zap.Error(err))
		}
	}

	index, egress := h.indexNetworks(ctx, listed, byRegion)
	egressIndex := newEgressNetworkIndex(egress)
	for _, region := range regions {
		rctx := middleware.ContextWithRegion(ctx, region)
		h.adoptRegionNetworks(rctx, region, byRegion[region], index)
		// An adopted network keeps the --internal flag it was created with,
		// which a gateway attached or detached since may contradict. This is
		// the one pass that repairs it: a repair recreates the network under
		// its containers, which is a startup's to do and not a placement's
		// (see ensureRegionReconciled).
		h.reconcileVPCNetworkIsolation(rctx)
		// Then the region's egress networks, and the placements whose route
		// tables may have changed while Overcast was down. After the
		// isolation repair, so a plane recreated under its containers is
		// settled before their route out is revisited.
		h.reconcileRegionEgressNetworks(rctx, byRegion[region], egressIndex)
		h.joinRegionNetworks(rctx)
	}
	// Only now is a network known to be unclaimed: by no VPC in any region.
	h.removeOrphanedNetworks(ctx, h.unrecorded(ctx, olderThanScan(index.unclaimed(), snapshot)), h.orphanLogPrefix())
	h.sweepEgressNetworks(ctx, olderThanScan(egressIndex.unclaimed(), snapshot))
	h.reconciledAll.Store(true)
}

// unrecorded narrows sweep to what no record names now, on a second store
// read. olderThanScan covers the CreateVpc whose network is newer than the
// scan; this covers the other direction — a network the snapshot already held
// whose record landed after the scan — since nothing serialises CreateVpc
// against this pass. When the store cannot be read again nothing is swept: an
// unremovable orphan is the cheaper mistake.
func (h *Handler) unrecorded(ctx context.Context, sweep map[string]*docker.NetworkSummary) map[string]*docker.NetworkSummary {
	if len(sweep) == 0 {
		return sweep
	}
	byRegion, aerr := h.store.vpcsByRegion(ctx)
	if aerr != nil {
		h.log.WithRecorder(ctx).Error("reconcile networks: list VPCs before the sweep; sweeping nothing",
			zap.String("error", aerr.Message))
		return nil
	}
	for _, vpcs := range byRegion {
		for _, vpc := range vpcs {
			delete(sweep, vpc.DockerNetworkID)
		}
	}
	return sweep
}

// olderThanScan narrows unclaimed to the networks snapshot already held:
// those that existed before the store scan, so that a record naming one, had
// there been one, would have been seen.
func olderThanScan(unclaimed map[string]*docker.NetworkSummary, snapshot []docker.NetworkSummary) map[string]*docker.NetworkSummary {
	out := make(map[string]*docker.NetworkSummary, len(unclaimed))
	for i := range snapshot {
		if n, ok := unclaimed[snapshot[i].ID]; ok {
			out[snapshot[i].ID] = n
		}
	}
	return out
}

// indexNetworks builds the index a pass adopts from: the daemon's snapshot
// narrowed to the networks this instance may act on, knowing every VPC the
// store holds in every region. The instance identity is published first, so
// that the narrowing has an identity to compare owners against.
func (h *Handler) indexNetworks(ctx context.Context, networks []docker.NetworkSummary, byRegion map[string][]*VPC) (*vpcNetworkIndex, []docker.NetworkSummary) {
	h.publishInstanceIdentity(ctx)
	// The index holds only the planes. A VPC's egress network
	// (OVERCAST_VPC_EGRESS=routed) carries the same resource-id label and
	// would be adopted as the plane, or swept as an orphan; it has its own
	// pass — see splitVPCNetworks and reconcileRegionEgressNetworks.
	planes, egress := splitVPCNetworks(h.networksInScope(ctx, networks))
	return newVPCNetworkIndex(planes, byRegion), egress
}

// adoptRegionNetworks is one region's share of a pass: the strategy matches
// the region's records to networks in the index and creates what is missing.
// ctx carries the region, so every store read and write here lands in its
// partition.
func (h *Handler) adoptRegionNetworks(ctx context.Context, region string, vpcs []*VPC, index *vpcNetworkIndex) {
	index.enter(region)
	h.vpcStrategy.Reconcile(ctx, vpcs, index)
	h.clearBackedVPCs(vpcs)
}

// joinRegionNetworks attaches Overcast to every network the region's records
// name. Adoption takes networks that already existed, so joining only on
// create would leave Overcast off every network that survived a restart. The
// records are re-read: the passes before this one may have moved a record
// onto a new network.
func (h *Handler) joinRegionNetworks(ctx context.Context) {
	log := h.log.WithRecorder(ctx)

	vpcs, aerr := h.store.listVPCs(ctx)
	if aerr != nil {
		log.Error("reconcile networks: list VPCs after reconcile", zap.String("error", aerr.Message))
		return
	}
	for _, vpc := range vpcs {
		h.joinVPCNetwork(ctx, vpc.DockerNetworkID)
	}
}

// regionReconcile is a region's entry in Handler.reconciledRegions: covered
// by a per-region pass, or — done false — the time a failed attempt may be
// retried.
type regionReconcile struct {
	done    bool
	retryAt time.Time
}

// regionReconcileRetry is how long a region waits, after a per-region pass
// could not read the store or the daemon, before a placement tries again.
// Every placement into the region asks (dataplane.PlaceInVPC asks twice), and
// each attempt is a Docker round-trip serialised on reconcileMu, so without
// the wait a service scaling out during a daemon outage queues single file
// behind a list call that keeps failing.
const regionReconcileRetry = 30 * time.Second

// ensureRegionReconciled is the backstop to the full pass: while no full pass
// has run to the end since Docker last appeared, a region is reconciled on the
// first placement into it, before the caller reads a record that may name a
// network the daemon no longer has. Once a full pass has completed this is a
// single atomic load, and until then a region already covered costs a map
// lookup.
//
// The full pass covers every region the store held when it ran; this catches
// the case where it could not run at all — the store unreadable at the time —
// and the window between start and the first full pass reaching the store.
// A full pass on a later Docker reconnect leaves reconciledAll set until it
// begins, so it hands this no such window; it closes its own by listing the
// daemon after its store scan. Only this region's VPCs are matched, and the
// index is discarded rather than swept: what it leaves unclaimed is every
// other region's network, not litter.
//
// It is narrower than the full pass in one more way, deliberately: it adopts
// what exists and creates what is missing, and does not repair a network's
// isolation. A repair recreates the bridge under its containers, and the
// caller here is an ECS RunTask or a Lambda invoke holding a process-wide
// lock. A drifted network keeps its flag until the next full pass — the next
// start, or Docker reappearing — which is where the repair, and the advisory
// when it fails, already lives. What it adopts it does report, drift
// included (reportVPCNetworks): health lists a network from the placement
// that adopted it, not from the next full pass.
func (h *Handler) ensureRegionReconciled(ctx context.Context) {
	if h.reconciledAll.Load() || !h.dockerReady.Load() || h.docker == nil {
		return
	}
	region := h.store.region(ctx)
	if !h.regionReconcileDue(region) {
		return
	}
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	if h.reconciledAll.Load() || !h.regionReconcileDue(region) {
		return // the full pass, or another placement, got here first
	}
	h.reconcileRegionLocked(ctx, region)
}

// unbackedRetryInterval is how soon a VPC with no network may be retried:
// every launchability check on it asks, and each attempt is a Docker round-trip
// under reconcileMu.
const unbackedRetryInterval = 5 * time.Second

// healUnbackedVPC gives a VPC recorded `unbacked` another chance at a network,
// and returns the status afterwards. A create the daemon refused once — an
// address pool a stranded network held, Docker mid-restart — is otherwise
// final until Docker next reappears: the full pass has run, reconciledAll
// vouches for the region, and the first anyone hears of it is an RDS instance
// refused as "not launchable" long after the cause is gone. The pass is the
// region's own (adopt what exists, create what is missing), so a network that
// has since been freed or created is picked up exactly as it would be on a
// restart.
func (h *Handler) healUnbackedVPC(ctx context.Context, vpcID string) string {
	status := func() string {
		vpc, aerr := h.store.getVPC(ctx, vpcID)
		if aerr != nil {
			return ""
		}
		if vpc.NetworkStatus == "" {
			return vpcNetworkStatusOK
		}
		return vpc.NetworkStatus
	}
	if !h.dockerReady.Load() || h.docker == nil {
		return status()
	}
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	// Another caller may have healed it while this one waited for the lock.
	if st := status(); st != vpcNetworkStatusUnbacked {
		return st
	}
	now := h.clk.Now()
	if last, ok := h.unbackedRetries.Load(vpcID); ok && now.Before(last.(time.Time).Add(unbackedRetryInterval)) {
		return vpcNetworkStatusUnbacked
	}
	h.unbackedRetries.Store(vpcID, now)
	h.reconcileRegionLocked(ctx, h.store.region(ctx))
	st := status()
	if st != vpcNetworkStatusUnbacked {
		h.unbackedRetries.Delete(vpcID)
	}
	return st
}

// reconcileRegionLocked is the per-region pass: adopt the region's networks and
// create the missing ones. The caller holds reconcileMu.
func (h *Handler) reconcileRegionLocked(ctx context.Context, region string) {
	log := h.log.WithRecorder(ctx)

	// Every region's VPCs, not this one's: the index has to know which VPCs
	// the networks it holds still belong to, wherever those VPCs are. The
	// store is read before the daemon so that every record seen has a network
	// older than the snapshot — CreateVpc creates the network first and
	// records it after, and the other order could take a VPC created in
	// between for one whose network is missing.
	byRegion, aerr := h.store.vpcsByRegion(ctx)
	if aerr != nil {
		log.Error("reconcile networks: list VPCs on first use of a region",
			zap.String("region", region), zap.String("error", aerr.Message),
			zap.Duration("retry_after", regionReconcileRetry))
		h.deferRegionReconcile(region)
		return
	}
	networks, err := h.docker.ListNetworks(ctx, serviceName)
	if err != nil {
		log.Warn("reconcile networks: list networks on first use of a region",
			zap.String("region", region), zap.Error(err),
			zap.Duration("retry_after", regionReconcileRetry))
		h.deferRegionReconcile(region)
		return
	}
	vpcs := byRegion[region]
	log.Info("reconcile networks: region not covered since Docker appeared — reconciling it on first use",
		zap.String("region", region), zap.Int("vpcs", len(vpcs)))
	index, _ := h.indexNetworks(ctx, networks, byRegion)
	h.adoptRegionNetworks(ctx, region, vpcs, index)
	h.joinRegionNetworks(ctx)
	h.reportVPCNetworks(ctx)
	h.reconciledRegions.Store(region, regionReconcile{done: true})
}

// regionReconcileDue reports whether region still needs a per-region pass:
// none has covered it since the last full pass started, and no failed attempt
// is still holding it back.
func (h *Handler) regionReconcileDue(region string) bool {
	v, ok := h.reconciledRegions.Load(region)
	if !ok {
		return true
	}
	r, ok := v.(regionReconcile)
	if !ok {
		return true
	}
	return !r.done && !h.clk.Now().Before(r.retryAt)
}

// deferRegionReconcile records a failed attempt on region: until
// regionReconcileRetry has passed, a placement answers from the record as it
// stands rather than asking the daemon again.
func (h *Handler) deferRegionReconcile(region string) {
	h.reconciledRegions.Store(region, regionReconcile{retryAt: h.clk.Now().Add(regionReconcileRetry)})
}

// orphanLogPrefix names the strategy in the orphan sweep's log lines, as each
// strategy's own messages do: nothing for the default, the name for the rest.
func (h *Handler) orphanLogPrefix() string {
	if name := h.vpcStrategy.Name(); name != "" && name != "shared" {
		return name + ": "
	}
	return ""
}

// nsInstance holds this instance's sweep-domain identity, stamped into
// docker.LabelInstance on every VPC network it creates. See
// serviceutil.InstanceDomain.
const nsInstance = "ec2:instance"

// instanceDomain is this instance's identity, or "" when the store cannot
// establish one — in which case nothing is ours to remove.
func (h *Handler) instanceDomain(ctx context.Context) string {
	if h.instances == nil {
		return ""
	}
	return h.instances.Resolve(ctx)
}

// networksInScope narrows a daemon-wide snapshot of EC2 networks to the ones
// this instance may act on.
//
// The snapshot is every network on the daemon carrying overcast.service=ec2,
// whoever created it. Two Overcasts sharing a daemon — a developer's live
// instance and a test server, or two agents' instances — each see the other's
// VPC networks in it, and unfiltered the strategies' orphan pass removed them:
// a network is "orphaned" by having no VPC record in *this* store, and the
// other instance's records are not here. That is how a test run took a live
// instance's overcast-vpc-* network with it.
//
// The rule is the container GC's (docker.LabelInstance). A network labelled
// with another instance's identity is invisible: neither adopted nor removed.
// One labelled with ours is fully in scope. One with no label predates the
// label — it stays adoptable, so a VPC that survived an upgrade still finds its
// network, but removeOrphanedNetworks leaves it, because absence is not
// permission.
func (h *Handler) networksInScope(ctx context.Context, networks []docker.NetworkSummary) []docker.NetworkSummary {
	domain := h.instanceDomain(ctx)
	log := h.log.WithRecorder(ctx)
	in := make([]docker.NetworkSummary, 0, len(networks))
	for _, n := range networks {
		if owner := n.Instance(); owner != "" && owner != domain {
			log.Debug("reconcile networks: leaving a network another instance owns",
				zap.String("network", n.ID), zap.String("name", n.Name), zap.String("owner", owner))
			continue
		}
		in = append(in, n)
	}
	return in
}

// removeOrphanedNetworks removes what the strategy's adoption passes — one
// per region, over one index — left unclaimed in byID, and only what this
// instance created. networksInScope has already dropped other instances'
// networks, so what remains is ours or unlabelled; the unlabelled are retained
// and said so, since their owner cannot be established and an unremovable
// orphan is a far cheaper mistake than deleting a network another instance is
// serving. prefix names the strategy in the log, as each strategy's own
// messages do.
func (h *Handler) removeOrphanedNetworks(ctx context.Context, byID map[string]*docker.NetworkSummary, prefix string) {
	h.sweepUnclaimed(ctx, byID, prefix, func(n *docker.NetworkSummary) {
		if err := h.removeDockerVPCNetwork(ctx, n.ID); err != nil {
			h.log.WithRecorder(ctx).Warn("reconcile networks: "+prefix+"remove orphaned network",
				zap.String("network", n.ID),
				zap.Error(err))
		}
	})
}

// sweepUnclaimed removes the networks in byID that this instance created, and
// retains the rest, saying which it did either way. What "removes" means is
// the caller's — a VPC plane and a VPC egress network are taken down
// differently — but the ownership rule is not, and it is the part that must
// not drift: a network whose owner cannot be established belongs to a run this
// one knows nothing about, and absence of a label is never permission.
func (h *Handler) sweepUnclaimed(ctx context.Context, byID map[string]*docker.NetworkSummary, prefix string, remove func(*docker.NetworkSummary)) {
	log := h.log.WithRecorder(ctx)
	domain := h.instanceDomain(ctx)
	for id, n := range byID {
		if domain == "" || n.Instance() != domain {
			log.Info("reconcile networks: "+prefix+"retaining an unclaimed network whose owner cannot be established",
				zap.String("vpc", n.ResourceID()),
				zap.String("network", id),
				zap.String("name", n.Name))
			continue
		}
		log.Info("reconcile networks: "+prefix+"removing orphaned network",
			zap.String("vpc", n.ResourceID()),
			zap.String("network", id),
			zap.String("name", n.Name))
		remove(n)
	}
}

// joinVPCNetwork attaches Overcast itself to a VPC's Docker network.
//
// A VPC network is a private bridge Overcast is not on by default, so anything
// in-process that has to *reach* a task — the ELB data plane forwarding to an
// awsvpc target, most of all — cannot dial the address the task actually has.
// Overcast already joins the ECS and Lambda networks for the same reason; this
// is that, per VPC. Connecting is idempotent, so callers may repeat it freely.
//
// Only meaningful when Overcast is itself containerised: on the host there is
// no container to attach, and reaching a bridge subnet is the host's routing
// question rather than ours.
func (h *Handler) joinVPCNetwork(ctx context.Context, netID string) {
	log := h.log.WithRecorder(ctx)

	self, ok := h.selfContainer()
	if !ok || netID == "" {
		return
	}
	if err := h.docker.ConnectNetwork(ctx, netID, self); err != nil {
		log.Warn("vpc network: could not attach Overcast to the VPC network — "+
			"the load balancer data plane cannot reach tasks in this VPC",
			zap.String("network", netID), zap.Error(err))
		return
	}
	log.Debug("vpc network: Overcast attached", zap.String("network", netID))
}

// leaveVPCNetwork detaches Overcast from a VPC network. Docker refuses to
// remove a network that still has endpoints, so the join above would otherwise
// make every VPC network undeletable.
func (h *Handler) leaveVPCNetwork(ctx context.Context, netID string) {
	log := h.log.WithRecorder(ctx)

	self, ok := h.selfContainer()
	if !ok || netID == "" {
		return
	}
	if err := h.docker.DisconnectNetwork(ctx, netID, self); err != nil {
		// Not being attached is the common case here (host mode, a network
		// created before this ran), and Docker reports it as an error.
		log.Debug("vpc network: detach Overcast",
			zap.String("network", netID), zap.Error(err))
	}
}

// The two environment probes behind selfContainer, indirected so a test can
// stand in for a machine it cannot arrange: whether this process is
// containerised, and which container that is.
var (
	runningInContainer = containerendpoint.RunningInContainer
	ownHostname        = os.Hostname
)

// selfContainer returns the container Overcast is running in. Docker accepts a
// container's short ID where it accepts a name, and inside a container the
// hostname is that short ID unless it was overridden — the same identification
// containerendpoint uses to find Overcast's address on the ECS network.
func (h *Handler) selfContainer() (string, bool) {
	if !h.dockerReady.Load() || h.docker == nil || !runningInContainer() {
		return "", false
	}
	hostname, err := ownHostname()
	if err != nil || hostname == "" {
		return "", false
	}
	return hostname, true
}

// createDockerVPCNetwork creates a Docker bridge network for the given VPC
// using its CidrBlock (or DockerCidrBlock if the active strategy has set one).
//
// Whether it is `--internal` follows OVERCAST_VPC_EGRESS, not the VPC's
// internet gateway: `open` (the default) leaves every network routable and
// `none` isolates every one of them. The gateway is read and passed through
// because `routed` will decide from it, and because the fact belongs in the
// decision even while the mode ignores it. See dataplane.VPCNetworkInternal.
//
// Returns the Docker network ID on success.
func (h *Handler) createDockerVPCNetwork(ctx context.Context, vpc *VPC) (string, error) {
	// The last mutation path that did not serialise: CreateVpc, and the
	// strategies' reconcile, raced `overcast network reset` and each other on
	// the same name.
	//
	// The lock is taken here and not in createDockerVPCNetworkInternal, which is
	// the primitive the gateway flip and the startup isolation pass call *while
	// already holding it* — a sync.Mutex is not reentrant, and taking it twice
	// deadlocks the reconcile.
	defer docker.LockNetwork(h.cfg.VPCNetwork(vpc.VpcID))()

	hasGateway := h.vpcHasInternetGateway(ctx, vpc.VpcID)
	netID, err := h.createDockerVPCNetworkInternal(ctx, vpc,
		dataplane.VPCNetworkInternal(h.cfg, hasGateway), hasGateway)
	if err != nil {
		h.noteUnbackedVPC(vpc.VpcID, err)
		return "", err
	}
	h.netProblems.Delete(unbackedProblemKey(vpc.VpcID))
	return netID, nil
}

// unbackedProblemKey separates a VPC's "no network at all" problem from the
// isolation and egress problems recorded against the same VPC, so clearing one
// cannot clear another.
func unbackedProblemKey(vpcID string) string { return vpcID + "/unbacked" }

// noteUnbackedVPC records that the daemon refused to create vpcID's network,
// with the reason it gave, for the health advisories.
//
// The strategies swallow the failure: CreateVpc answers 200 and the record is
// written `unbacked`, because a VPC is metadata AWS never refuses and the next
// reconcile may well succeed where this create did not (Docker was mid-restart,
// say). What they must not do is swallow it *silently*. A VPC with no network
// is a VPC nothing can be placed in, and without this the first sign was an
// RDS instance failing its launchability check minutes later, or an ECS task
// failing to find a network that was never made — far from the cause, which
// the advisory now names the moment it happens.
func (h *Handler) noteUnbackedVPC(vpcID string, err error) {
	h.netProblems.Store(unbackedProblemKey(vpcID), dataplane.VPCNetworkProblem{
		VpcID:    vpcID,
		Unbacked: true,
		Detail:   fmt.Sprintf("the Docker network could not be created: %v", err),
	})
}

// clearBackedVPCs drops the unbacked problem of every VPC in vpcs that now
// names a network. Adoption backs a VPC without a create — the network was
// there all along, or another VPC's is being shared — so the create path's own
// clearing does not reach it.
func (h *Handler) clearBackedVPCs(vpcs []*VPC) {
	for _, vpc := range vpcs {
		if vpc != nil && vpc.DockerNetworkID != "" {
			h.netProblems.Delete(unbackedProblemKey(vpc.VpcID))
		}
	}
}

// createDockerVPCNetworkInternal is createDockerVPCNetwork with the isolation
// supplied rather than derived, together with the internet-gateway fact it was
// derived *from*.
//
// Both, not one. The gateway fact is what gets recorded on the network for
// readers with no state store (docker.LabelGatewayAttached), and recovering it
// by inverting the isolation only works while `internal == !hasGateway` — true
// under `open`, vacuous under `none`, and wrong under `routed`, where a subnet
// with a NAT route is routable with no internet gateway at all. Carrying the
// fact costs one parameter and cannot go stale.
func (h *Handler) createDockerVPCNetworkInternal(ctx context.Context, vpc *VPC, internal, hasGateway bool) (string, error) {
	spec := h.vpcNetworkSpec(ctx, vpc, internal, hasGateway)
	netID, err := h.docker.CreateNetworkWithOptions(ctx, spec.CreateOptions())
	if err != nil {
		return "", err
	}
	// Recorded on the record, not resolved from Docker later: the flip lock
	// keys on it, and the one moment it would need resolving is the moment the
	// network does not exist. See Handler.vpcNetworkLockKey.
	vpc.DockerNetworkName = spec.Name
	// The same reasoned line the planes get from docker.Probe. A VPC network is
	// not in PlaneSpecs and its spec has no InternalMode, so without this it is
	// the one network class whose isolation is never explained anywhere — and
	// the one where `Internal=true` beside a container with egress most needs
	// explaining.
	h.log.WithRecorder(ctx).Info("vpc network isolation",
		zap.String("vpc", vpc.VpcID),
		zap.String("network", spec.Name),
		zap.Bool("internal", internal),
		zap.String("reason", dataplane.VPCNetworkEgressReason(h.cfg, internal)))
	h.recordNetworkStatus(docker.NetworkStatus{
		Name:     spec.Name,
		Internal: internal,
		Reason:   dataplane.VPCNetworkEgressReason(h.cfg, internal),
		SpecHash: spec.SpecHash(),
	})
	h.joinVPCNetwork(ctx, netID)
	return netID, nil
}

// vpcNetworkSpec is the complete desired state of one VPC's Docker network,
// resolved and ready either to create from or to compare a live network
// against. One definition for both, so a network cannot be created in a state
// the verification would then call wrong.
//
// `internal` is what the caller decided and `hasGateway` is the fact it decided
// from; every caller here ran the second through dataplane.VPCNetworkInternal to
// get the first. Both are passed rather than re-read, because the gateway flip
// runs *before* it records the attachment — a fresh store read there returns the
// state being replaced, and would stamp a label that contradicts the isolation
// beside it.
//
// The fact is recorded on the network (docker.LabelGatewayAttached) so
// `overcast network status` — which has no state store to ask — can compute this
// same desired state instead of guessing at it.
func (h *Handler) vpcNetworkSpec(ctx context.Context, vpc *VPC, internal, hasGateway bool) docker.ResolvedNetworkSpec {
	return dataplane.VPCNetworkSpec(h.cfg, dataplane.VPCNetwork{
		VPCID:              vpc.VpcID,
		Subnet:             preferredDockerSubnet(vpc),
		Owner:              h.instanceDomain(ctx),
		Internal:           internal,
		HasInternetGateway: hasGateway,
	}).Resolve(ctx, h.docker)
}

// removeDockerVPCNetwork removes a Docker network by ID. Missing networks
// are treated as success.
func (h *Handler) removeDockerVPCNetwork(ctx context.Context, netID string) error {
	if netID == "" {
		return nil
	}
	h.leaveVPCNetwork(ctx, netID)
	return h.docker.RemoveNetwork(ctx, netID)
}

// vpcHasInternetGateway returns true if any internet gateway is attached to
// the given VPC.
func (h *Handler) vpcHasInternetGateway(ctx context.Context, vpcID string) bool {
	igws, err := h.store.listInternetGateways(ctx)
	if err != nil {
		return false
	}
	for _, igw := range igws {
		for _, att := range igw.Attachments {
			if att.VpcID == vpcID && att.State == "attached" {
				return true
			}
		}
	}
	return false
}

// ── Internet-gateway isolation ───────────────────────────────────────────────
//
// A VPC network is `--internal` until an internet gateway is attached, and
// Docker fixes that flag when the network is created. Flipping it therefore
// means recreating the network — under whatever is already on it.

// vpcNetworkEndpoint is one container's attachment to a VPC network, captured
// before the network is recreated so the container can be put back exactly as
// it was: same address, same DNS names.
type vpcNetworkEndpoint struct {
	containerID string
	ipv4        string
	aliases     []string
}

// rejoinError is a flip that recreated the network but could not put every
// container back on it. The network carries the flag that was asked for, so
// the flip counts as done; the containers named are the problem.
type rejoinError struct {
	netID string
	err   error
}

func (e *rejoinError) Error() string {
	return fmt.Sprintf("containers could not rejoin the recreated network %s: %v", e.netID, e.err)
}

func (e *rejoinError) Unwrap() error { return e.err }

// changeVPCGateway applies an internet-gateway attach or detach to vpcID's
// Docker network and then records it, in that order and under the network's
// lock, so the record never names a gateway the network does not reflect:
// a flip that cannot be completed fails the call with nothing recorded, and
// the same call can be retried.
//
// record persists the attachment change. It runs under the same lock as the
// flip, so a sharer's detach cannot read the store between this VPC's flip
// and its record and undo it (#1569).
//
// Two VPCs are exempt from the flip and only recorded: one with no Docker
// network (the flag is derived from the gateway state when the network is
// eventually created), and the default VPC, whose network is the shared data
// plane — it already has the internet, and recreating it would take every
// container Overcast started with it.
func (h *Handler) changeVPCGateway(ctx context.Context, vpcID string, attach bool, record func() *protocol.AWSError) *protocol.AWSError {
	vpc, unlock, aerr := h.lockVPCNetwork(ctx, vpcID)
	if aerr != nil {
		return aerr
	}
	defer unlock()

	if vpc.IsDefault {
		h.log.WithRecorder(ctx).Warn("ignoring an internet-gateway change on the default VPC's network — "+
			"it is the shared data plane and cannot be recreated under running containers",
			zap.String("vpc", vpcID), zap.Bool("attach", attach))
		return record()
	}
	if !h.dockerReady.Load() || vpc.DockerNetworkID == "" {
		return record()
	}

	// A network several VPCs share is external while any of them has a
	// gateway. The shared strategy enforces no isolation between sharers to
	// begin with, so the most permissive of them sets the mode for all — the
	// alternative, one VPC's detach cutting off another's internet, is a
	// surprise nothing in the AWS model prepares a stack for. Only the shared
	// strategy can produce a sharer, so for strict and remapped this is a
	// per-VPC flip.
	hasGateway := attach
	if !attach {
		vpcs, attached, aerr := h.gatewayView(ctx)
		if aerr != nil {
			return aerr
		}
		hasGateway = networkHasGateway(vpcs, attached, vpc.DockerNetworkID, vpc.VpcID)
		if hasGateway {
			h.log.WithRecorder(ctx).Info("vpc network: staying external — another VPC on the shared network still has an internet gateway",
				zap.String("vpc", vpc.VpcID), zap.String("network", vpc.DockerNetworkID))
		}
	}
	// The gateway is a fact about the template. Under `open` it still decides
	// this network's `--internal` flag — what it no longer decides on its own is
	// *egress*, because the container is also on the routable control plane.
	// Under `none` the flag is true whatever the gateway says, and under
	// `routed` so is it: there the gateway decides which *subnets* route out,
	// which is a second network rather than this flag. See
	// dataplane.VPCNetworkInternal and docs/networking/egress.md.
	internal := dataplane.VPCNetworkInternal(h.cfg, hasGateway)

	if netID, err := h.applyVPCNetworkInternal(ctx, vpc, internal, hasGateway); err != nil {
		// A flip that stopped before the removal changed nothing, and the
		// record still describes the network; one that lost the network on
		// the way is recorded for the advisories, since nothing about it
		// heals on its own.
		if netID != vpc.DockerNetworkID {
			h.noteNetworkProblem(vpc.VpcID, netID,
				fmt.Sprintf("the Docker network should be %s but could not be recreated: %v", h.isolationWord(internal), err))
		}
		return vpcNetworkFlipError(attach, vpcID, err)
	}
	if aerr := record(); aerr != nil {
		return aerr
	}
	// Under `routed` the gateway decides nothing about the plane — it stays
	// internal — but everything about which subnets now route out through
	// it. Still under the lock, and after the record, so the route tables
	// read here see the attachment as it now stands.
	if dataplane.Routed(h.cfg) {
		if current, aerr := h.store.getVPC(ctx, vpcID); aerr == nil {
			h.reconcileVPCEgressLocked(ctx, current)
		}
	}
	return nil
}

// vpcNetworkFlipError is the answer when the VPC's Docker network could not be
// brought in line with the gateway change. AWS has no such failure — its
// gateways are metadata — so the code is Overcast's own InternalError, and the
// message says what Docker refused rather than hiding it behind the generic
// wording, because the remedy is on the user's side of the socket.
func vpcNetworkFlipError(attach bool, vpcID string, err error) *protocol.AWSError {
	verb := "attached to"
	if !attach {
		verb = "detached from"
	}
	return &protocol.AWSError{
		Code: protocol.ErrInternalError.Code,
		Message: fmt.Sprintf("The internet gateway was not %s vpc '%s': its Docker network could not be brought to the new isolation mode: %v",
			verb, vpcID, err),
		HTTPStatus: protocol.ErrInternalError.HTTPStatus,
	}
}

// lockVPCNetwork returns vpcID's current record with its Docker network's flip
// lock held.
//
// The lock is per network rather than per VPC because sharers must contend with
// each other: two gateway calls on two VPCs of one network, or a call racing
// the startup reconcile, would otherwise both disconnect the same endpoints and
// both try to remove and recreate the same subnet, with the loser marking every
// record on it unbacked.
//
// **Keyed by the network's name, not its ID, and that is the whole point.** A
// flip removes the network and creates it again, so the ID it started with is
// gone by the time renameVPCNetwork moves the records onto the successor — and
// a holder keyed by the old ID goes on holding a mutex nothing else will ever
// look up. A sharer arriving in the window between the recreate and the record
// write would take a different, free mutex and proceed straight into the same
// endpoints. The name survives the recreate (recreateDockerVPCNetwork keeps
// it), so a successor ID resolves to the same key and the exclusion holds all
// the way through record().
//
// It is also the lock docker.EnsureNetwork and `overcast network reset` take,
// for the same networks. One lock, or the startup verification and the gateway
// flip would each be serialised only against themselves.
//
// The record is re-read after the lock is taken — the network the caller
// resolved may have been replaced while it waited — and the acquisition
// restarts only when the replacement is a *different network*; a new ID under
// the same name is the flip this lock just waited out, and re-locking would
// spin. A VPC with no network needs no lock; unlock is then a no-op.
func (h *Handler) lockVPCNetwork(ctx context.Context, vpcID string) (*VPC, func(), *protocol.AWSError) {
	const attempts = 8
	for range attempts {
		vpc, aerr := h.store.getVPC(ctx, vpcID)
		if aerr != nil {
			return nil, func() {}, aerr
		}
		if vpc.DockerNetworkID == "" || vpc.IsDefault {
			// The default VPC's network is the shared data plane, which no
			// gateway change ever recreates (defaultVPCGuard). Nothing to
			// serialise, and asking the daemon its name would be a Docker call
			// made purely to lock something that is never touched.
			return vpc, func() {}, nil
		}
		key := h.vpcNetworkLockKey(vpc)
		unlock := docker.LockNetwork(key)
		current, aerr := h.store.getVPC(ctx, vpcID)
		if aerr != nil {
			unlock()
			return nil, func() {}, aerr
		}
		if current.DockerNetworkID == vpc.DockerNetworkID || h.vpcNetworkLockKey(current) == key {
			return current, unlock, nil
		}
		unlock() // a different network now: lock the one it is actually on
	}
	return nil, func() {}, protocol.Wrap(protocol.ErrInternalError,
		fmt.Errorf("vpc %s: its Docker network kept changing while waiting to lock it", vpcID))
}

// vpcNetworkLockKey is the name lockVPCNetwork serialises on, taken from the
// record rather than from Docker.
//
// Asking Docker would reintroduce the race the name-keying exists to close:
// during a flip the network is removed before the successor id is written, so a
// sharer reading the old id in that window would get a 404 from the inspect,
// fall back to keying on the id, and take a different and free mutex. It also
// put an inspect on every acquisition, and on each of the eight retries.
func (h *Handler) vpcNetworkLockKey(vpc *VPC) string {
	if vpc == nil {
		return ""
	}
	if vpc.DockerNetworkName != "" {
		return vpc.DockerNetworkName
	}
	// A record from before the name was written down. The derivable name is
	// right under `strict` and `remapped`, and right under `shared` for the VPC
	// that created the network — which is the one whose name it carries.
	return h.cfg.VPCNetwork(vpc.VpcID)
}

// gatewayView reads, once, what a flip decision needs: every VPC record, and
// the set of VPC IDs with an internet gateway attached.
func (h *Handler) gatewayView(ctx context.Context) ([]*VPC, map[string]bool, *protocol.AWSError) {
	vpcs, aerr := h.store.listVPCs(ctx)
	if aerr != nil {
		return nil, nil, aerr
	}
	igws, aerr := h.store.listInternetGateways(ctx)
	if aerr != nil {
		return nil, nil, aerr
	}
	attached := make(map[string]bool)
	for _, igw := range igws {
		for _, att := range igw.Attachments {
			if att.State == "attached" {
				attached[att.VpcID] = true
			}
		}
	}
	return vpcs, attached, nil
}

// networkHasGateway reports whether any VPC backed by netID, other than
// excludeVpcID, has an internet gateway attached. Under the shared strategy
// several VPCs can name one network; under the others this is one lookup.
func networkHasGateway(vpcs []*VPC, attached map[string]bool, netID, excludeVpcID string) bool {
	for _, v := range vpcs {
		if v.VpcID != excludeVpcID && v.DockerNetworkID == netID && attached[v.VpcID] {
			return true
		}
	}
	return false
}

// applyVPCNetworkInternal flips vpc's network and brings every stored record
// that named the old network — vpc and, under the shared strategy, its
// sharers — to the one that replaced it. The caller holds the network's flip
// lock (lockVPCNetwork). It returns the network that backs the VPC afterwards.
//
// Three outcomes. Success: the records follow the network and any problem
// recorded against them is cleared. A recreate whose containers could not all
// rejoin (rejoinError): the network carries the flag that was asked for, so
// the records follow it, the containers are recorded as a problem for the
// advisories, and the caller sees success — the alternative, failing the call
// over a network already in the requested state, is the inconsistency this
// whole path exists to prevent. Any other failure is returned as is; the
// caller decides whether it is worth recording, since the API path changes
// nothing when the flip stops before the removal.
func (h *Handler) applyVPCNetworkInternal(ctx context.Context, vpc *VPC, internal, hasGateway bool) (string, error) {
	log := h.log.WithRecorder(ctx)

	oldID := vpc.DockerNetworkID
	netID, err := h.flipDockerVPCNetworkInternal(ctx, vpc, internal, hasGateway)
	moved := []string{vpc.VpcID}
	if netID != oldID {
		moved = h.renameVPCNetwork(ctx, oldID, netID)
	}

	var rejoin *rejoinError
	switch {
	case err == nil:
		for _, id := range moved {
			h.netProblems.Delete(id)
		}
		if netID != oldID {
			log.Info("vpc network: isolation changed",
				zap.String("vpc", vpc.VpcID), zap.Bool("internal", internal),
				zap.String("old_network", oldID), zap.String("network", netID))
		}
		// The flip's own recreate path does not go through the create that
		// reports; without this, health keeps the isolation from before it.
		h.recordNetworkStatus(docker.NetworkStatus{
			Name:     h.vpcNetworkLockKey(vpc),
			Internal: internal,
			Reason:   dataplane.VPCNetworkEgressReason(h.cfg, internal),
			SpecHash: h.vpcNetworkSpec(ctx, vpc, internal, hasGateway).SpecHash(),
		})
		return netID, nil
	case errors.As(err, &rejoin):
		detail := fmt.Sprintf("the Docker network was recreated %s, but %v", h.isolationWord(internal), rejoin.err)
		for _, id := range moved {
			h.noteNetworkProblem(id, netID, detail)
		}
		log.Error("vpc network: isolation changed, but containers could not rejoin the recreated network",
			zap.String("vpc", vpc.VpcID), zap.String("network", netID), zap.Error(rejoin.err))
		return netID, nil
	default:
		log.Error("vpc network: could not change the network's isolation — "+
			"containers in this VPC keep the internet reachability they had",
			zap.String("vpc", vpc.VpcID), zap.String("network", oldID),
			zap.Bool("internal", internal), zap.Error(err))
		return netID, err
	}
}

// isolationWord names a flag in a problem or advisory, with the reason the
// mode gives for it.
func (h *Handler) isolationWord(internal bool) string {
	switch {
	case !internal:
		return "external (an internet gateway is attached)"
	case dataplane.Routed(h.cfg):
		return "--internal (OVERCAST_VPC_EGRESS=routed keeps every VPC plane internal; egress is a second network)"
	case dataplane.EgressMode(h.cfg) == config.VPCEgressNone:
		return "--internal (OVERCAST_VPC_EGRESS=none)"
	default:
		return "--internal (no internet gateway is attached)"
	}
}

// flipDockerVPCNetworkInternal makes vpc's Docker network carry the given
// `--internal` flag, recreating it when it does not already. It returns the
// network that backs the VPC afterwards — the same ID when nothing needed
// doing, empty when the VPC has been left with none.
//
// Docker refuses to remove a network that still has endpoints, so the
// containers on it are moved rather than being a reason to give up: each is
// disconnected, the network is recreated, and each is reconnected with the
// address and DNS aliases it had, so nothing that had resolved or dialled it
// has to notice. Every container on the network is moved, including one a
// user attached by hand — it is exactly what would otherwise block the
// removal. Their control-plane attachment is untouched throughout (see
// internal/dataplane), so an in-flight invocation keeps its Runtime API
// connection; only connections across the VPC bridge itself are dropped, as
// they are on AWS when routing changes under a live ENI.
//
// Failure leaves things as they were wherever there is a way back: a refused
// disconnect or removal reconnects what had already been moved and returns
// with the original network intact. A failed recreate is the case with none —
// the containers are put on a network with the old flag if one can be made,
// and the error says so. A recreate that succeeded but could not take every
// container back is a rejoinError: the caller treats the flip as done.
func (h *Handler) flipDockerVPCNetworkInternal(ctx context.Context, vpc *VPC, internal, hasGateway bool) (string, error) {
	log := h.log.WithRecorder(ctx)

	info, err := h.docker.InspectNetwork(ctx, vpc.DockerNetworkID)
	if err != nil {
		if docker.IsNotFound(err) {
			// The record names a network that no longer exists; the flag it
			// needs is known, so this is a plain create.
			return h.createDockerVPCNetworkInternal(ctx, vpc, internal, hasGateway)
		}
		return vpc.DockerNetworkID, fmt.Errorf("inspect network %s: %w", vpc.DockerNetworkID, err)
	}
	// Not `info.Internal == internal`. Docker's create call returns an existing
	// network unchanged, so a network adopted from an older Overcast, a
	// different egress mode, or another tool keeps every setting it was born
	// with — driver, subnet, driver options — while its isolation happens to
	// look right. Comparing the whole spec is what makes an adopted network
	// verified rather than assumed, and the recreate below is the repair: it
	// moves the containers across and rejoins them, which is why this can
	// afford to be strict.
	spec := h.vpcNetworkSpec(ctx, vpc, internal, hasGateway)
	diffs := spec.Diff(info)
	if len(diffs) == 0 {
		// Reported even when nothing changed: a health endpoint that lists only
		// the networks it had to touch cannot distinguish a healthy one from one
		// nobody looked at.
		h.recordNetworkStatus(docker.NetworkStatus{
			Name:     info.Name,
			Internal: info.Internal,
			Reason:   dataplane.VPCNetworkEgressReason(h.cfg, info.Internal),
			SpecHash: spec.SpecHash(),
		})
		return info.ID, nil
	}
	log.Info("vpc network: not in the state this configuration asks for — recreating it",
		zap.String("vpc", vpc.VpcID), zap.String("network", info.ID),
		zap.Strings("differs", docker.DiffStrings(diffs)))

	endpoints, err := h.vpcNetworkEndpoints(ctx, info)
	if err != nil {
		return info.ID, err
	}
	var moved []vpcNetworkEndpoint
	for _, ep := range endpoints {
		if err := h.docker.DisconnectNetwork(ctx, info.ID, ep.containerID); err != nil {
			h.restoreVPCNetworkEndpoints(ctx, info.ID, moved)
			return info.ID, fmt.Errorf("disconnect container %s from network %s: %w", ep.containerID, info.ID, err)
		}
		moved = append(moved, ep)
	}
	if err := h.removeDockerVPCNetwork(ctx, info.ID); err != nil {
		h.joinVPCNetwork(ctx, info.ID)
		h.restoreVPCNetworkEndpoints(ctx, info.ID, moved)
		return info.ID, err
	}

	netID, err := h.recreateDockerVPCNetwork(ctx, vpc, info, internal, hasGateway)
	if err != nil {
		fallbackID, ferr := h.recreateDockerVPCNetwork(ctx, vpc, info, info.Internal,
			info.Labels[docker.LabelGatewayAttached] == "true")
		if ferr != nil {
			return "", fmt.Errorf("recreate network for %s: %w (and could not restore the old one: %v)", vpc.VpcID, err, ferr)
		}
		if rerr := h.reconnectVPCNetworkEndpoints(ctx, fallbackID, moved); rerr != nil {
			log.Error("vpc network: containers could not rejoin the restored network",
				zap.String("vpc", vpc.VpcID), zap.String("network", fallbackID), zap.Error(rerr))
		}
		return fallbackID, fmt.Errorf("recreate network for %s with internal=%t: %w (restored with internal=%t)", vpc.VpcID, internal, err, info.Internal)
	}
	if err := h.reconnectVPCNetworkEndpoints(ctx, netID, moved); err != nil {
		return netID, &rejoinError{netID: netID, err: err}
	}
	return netID, nil
}

// recreateDockerVPCNetwork creates the network that replaces info, with the
// new flag and everything else as it was: the same name, the same labels
// (the resource ID and the instance ownership stamp among them), the same
// subnet. Under the shared strategy the network is named for the VPC that
// created it, and whichever sharer flips it must not rename it out from
// under the others; and a reconcile after a restart adopts by ID, then label,
// then subnet, so the labels are what let it find the network again.
func (h *Handler) recreateDockerVPCNetwork(ctx context.Context, vpc *VPC, info *docker.NetworkInspect, internal, hasGateway bool) (string, error) {
	if info.Name == "" || len(info.Labels) == 0 {
		return h.createDockerVPCNetworkInternal(ctx, vpc, internal, hasGateway)
	}
	// The create call comes from the spec, not hand-built from a few of its
	// fields. Anything the spec pins — driver, IPv6, gateway, driver options —
	// is created *and* hashed together, so a network cannot end up carrying a
	// spec hash asserting a setting it was never given. That is the exact bug
	// class this whole change exists to close, and hand-copying four fields
	// reintroduces it the moment a fifth is pinned.
	opts := h.vpcNetworkSpec(ctx, vpc, internal, hasGateway).CreateOptions()

	// Two things are taken from the live network rather than the spec. Its
	// name, and its identity labels: under the shared strategy the network is
	// named and labelled for the VPC that created it, and a sharer flipping it
	// must not rename or relabel it out from under the others. The spec's own
	// labels win where they overlap, so the refreshed hash lands.
	opts.Name = info.Name
	labels := make(map[string]string, len(info.Labels)+len(opts.Labels))
	for k, v := range info.Labels {
		labels[k] = v
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	// ...except the ones that name the owner, which stay the owner's.
	for _, k := range []string{docker.LabelResourceID, "overcast.vpc-id", docker.LabelInstance} {
		if v, ok := info.Labels[k]; ok {
			labels[k] = v
		}
	}
	opts.Labels = labels

	netID, err := h.docker.CreateNetworkWithOptions(ctx, opts)
	if err != nil {
		return "", err
	}
	vpc.DockerNetworkName = opts.Name
	h.joinVPCNetwork(ctx, netID)
	return netID, nil
}

// vpcNetworkEndpoints captures every container on the network other than
// Overcast itself, which removeDockerVPCNetwork detaches and
// createDockerVPCNetworkInternal rejoins on its own. The address and aliases
// come from the container's side of the attachment; the network's own view
// carries neither reliably.
func (h *Handler) vpcNetworkEndpoints(ctx context.Context, info *docker.NetworkInspect) ([]vpcNetworkEndpoint, error) {
	self, _ := h.selfContainer()
	ids := make([]string, 0, len(info.Containers))
	for id := range info.Containers {
		if self != "" && strings.HasPrefix(id, self) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	endpoints := make([]vpcNetworkEndpoint, 0, len(ids))
	for _, id := range ids {
		c, err := h.docker.InspectContainer(ctx, id)
		if err != nil {
			if docker.IsNotFound(err) {
				continue // gone between the two inspects; nothing to move
			}
			return nil, fmt.Errorf("inspect container %s on network %s: %w", id, info.ID, err)
		}
		ep := vpcNetworkEndpoint{containerID: id}
		for name, n := range c.NetworkSettings.Networks {
			if n.NetworkID == info.ID || name == info.Name {
				ep.ipv4, ep.aliases = n.IPAddress, n.Aliases
				break
			}
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// reconnectVPCNetworkEndpoints puts endpoints back on netID with the address
// each had. Docker rejects a pinned address it cannot honour, so a container
// whose old address is somehow taken is reconnected with a fresh one rather
// than left off the network — its endpoint hostnames still resolve, only a
// caller holding the raw address would notice. Every endpoint is attempted;
// the error names the ones that failed.
func (h *Handler) reconnectVPCNetworkEndpoints(ctx context.Context, netID string, endpoints []vpcNetworkEndpoint) error {
	log := h.log.WithRecorder(ctx)

	var errs []error
	for _, ep := range endpoints {
		cfg := &docker.EndpointSettings{Aliases: ep.aliases}
		if ep.ipv4 != "" {
			cfg.IPAMConfig = &docker.EndpointIPAMConfig{IPv4Address: ep.ipv4}
		}
		err := h.docker.ConnectNetworkWithConfig(ctx, netID, ep.containerID, cfg)
		if err != nil && cfg.IPAMConfig != nil {
			log.Warn("vpc network: container could not keep its address across the recreate — reconnecting with a new one",
				zap.String("container", ep.containerID), zap.String("address", ep.ipv4),
				zap.String("network", netID), zap.Error(err))
			cfg.IPAMConfig = nil
			err = h.docker.ConnectNetworkWithConfig(ctx, netID, ep.containerID, cfg)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("container %s: %w", ep.containerID, err))
		}
	}
	return errors.Join(errs...)
}

// restoreVPCNetworkEndpoints is reconnectVPCNetworkEndpoints on the way back
// from a flip that could not proceed: the network is the one the containers
// were on, so a failure here is logged rather than returned — the caller is
// already reporting the error that stopped the flip.
func (h *Handler) restoreVPCNetworkEndpoints(ctx context.Context, netID string, endpoints []vpcNetworkEndpoint) {
	if err := h.reconnectVPCNetworkEndpoints(ctx, netID, endpoints); err != nil {
		h.log.WithRecorder(ctx).Error("vpc network: containers could not rejoin the network after an abandoned flip",
			zap.String("network", netID), zap.Error(err))
	}
}

// renameVPCNetwork moves every stored VPC record that names oldID onto newID
// and returns the IDs of the VPCs it moved. Under the shared strategy that is
// the owner and every sharer; elsewhere it is one record. An empty newID
// means the network is gone, and the records say so.
func (h *Handler) renameVPCNetwork(ctx context.Context, oldID, newID string) []string {
	log := h.log.WithRecorder(ctx)

	vpcs, aerr := h.store.listVPCs(ctx)
	if aerr != nil {
		log.Error("vpc network: list VPCs to record the recreated network", zap.String("error", aerr.Message))
		return nil
	}
	var moved []string
	for _, v := range vpcs {
		if v.DockerNetworkID != oldID {
			continue
		}
		v.DockerNetworkID = newID
		if newID == "" {
			v.NetworkStatus = vpcNetworkStatusUnbacked
		}
		if aerr := h.store.putVPC(ctx, v); aerr != nil {
			log.Error("vpc network: record the recreated network",
				zap.String("vpc", v.VpcID), zap.String("error", aerr.Message))
			continue
		}
		moved = append(moved, v.VpcID)
	}
	return moved
}

// reconcileVPCNetworkIsolation runs after the strategy has matched records to
// networks: a network adopted from a previous run carries whatever flag it was
// created with, and a gateway attached or detached while that run could not
// act on it leaves the two disagreeing. Each backed network is checked once,
// under its flip lock — inspect is the whole cost when they agree — and
// repaired when they do not. A repair that fails is recorded as a problem so
// it reaches the health advisories rather than only the startup log.
func (h *Handler) reconcileVPCNetworkIsolation(ctx context.Context) {
	log := h.log.WithRecorder(ctx)

	vpcs, attached, aerr := h.gatewayView(ctx)
	if aerr != nil {
		log.Error("reconcile networks: read VPCs and gateways for the isolation check", zap.String("error", aerr.Message))
		return
	}
	sort.Slice(vpcs, func(i, j int) bool { return vpcs[i].VpcID < vpcs[j].VpcID })

	checked := make(map[string]struct{}, len(vpcs))
	for _, listed := range vpcs {
		// The default VPC's network is the shared data plane, never a
		// per-VPC bridge; its gateway is metadata (see defaultVPCGuard).
		if listed.IsDefault || listed.DockerNetworkID == "" || listed.DockerNetworkID == h.cfg.Network {
			continue
		}
		if _, done := checked[listed.DockerNetworkID]; done {
			continue
		}
		vpc, unlock, aerr := h.lockVPCNetwork(ctx, listed.VpcID)
		if aerr != nil {
			log.Error("reconcile networks: lock VPC network", zap.String("vpc", listed.VpcID), zap.String("error", aerr.Message))
			continue
		}
		if _, done := checked[vpc.DockerNetworkID]; done || vpc.DockerNetworkID == "" {
			unlock()
			continue
		}
		checked[vpc.DockerNetworkID] = struct{}{}

		hasGateway := networkHasGateway(vpcs, attached, vpc.DockerNetworkID, "")
		internal := dataplane.VPCNetworkInternal(h.cfg, hasGateway)
		netID, err := h.applyVPCNetworkInternal(ctx, vpc, internal, hasGateway)
		if err != nil {
			h.noteNetworkProblem(vpc.VpcID, netID,
				fmt.Sprintf("the Docker network should be %s but could not be recreated: %v", h.isolationWord(internal), err))
		}
		checked[netID] = struct{}{}
		unlock()
	}
}

// reportVPCNetworks publishes to health the state of every network the
// region's records name, as it stands, repairing none. The full pass reports
// as a side effect of checking each network under its lock
// (reconcileVPCNetworkIsolation, through the flip); the lazy pass adopts and
// does not repair, so it reports here — or a network adopted on first use of
// a region would be absent from health until the next full pass, which for
// the backstop's usual case (the store unreadable at start, Docker fine) is
// the next restart. A mismatch it will not fix is reported as drift, naming
// what fixes it. No lock is taken: this only reads, and a flip in progress
// reports as the transient it is.
func (h *Handler) reportVPCNetworks(ctx context.Context) {
	if h.networkReporter == nil {
		return
	}
	log := h.log.WithRecorder(ctx)

	vpcs, attached, aerr := h.gatewayView(ctx)
	if aerr != nil {
		log.Error("reconcile networks: read VPCs and gateways to report networks", zap.String("error", aerr.Message))
		return
	}
	reported := make(map[string]struct{}, len(vpcs))
	for _, vpc := range vpcs {
		if vpc.IsDefault || vpc.DockerNetworkID == "" || vpc.DockerNetworkID == h.cfg.Network {
			continue
		}
		if _, done := reported[vpc.DockerNetworkID]; done {
			continue
		}
		reported[vpc.DockerNetworkID] = struct{}{}
		info, err := h.docker.InspectNetwork(ctx, vpc.DockerNetworkID)
		if err != nil {
			log.Warn("reconcile networks: inspect network to report it",
				zap.String("vpc", vpc.VpcID), zap.String("network", vpc.DockerNetworkID), zap.Error(err))
			continue
		}
		hasGateway := networkHasGateway(vpcs, attached, vpc.DockerNetworkID, "")
		spec := h.vpcNetworkSpec(ctx, vpc, dataplane.VPCNetworkInternal(h.cfg, hasGateway), hasGateway)
		status := docker.NetworkStatus{
			Name:     info.Name,
			Internal: info.Internal,
			Reason:   dataplane.VPCNetworkEgressReason(h.cfg, info.Internal),
			SpecHash: spec.SpecHash(),
		}
		if diffs := spec.Diff(info); len(diffs) > 0 {
			status.Mismatch = diffs
			status.Drift = fmt.Sprintf("adopted as found on first use of its region; not in the configured state (%s) — the next startup reconcile repairs it",
				strings.Join(docker.DiffStrings(diffs), "; "))
			status.Fix = vpcNetworkFix(info)
		}
		h.recordNetworkStatus(status)
	}
}

// noteNetworkProblem records that vpcID's network is not in the state its
// gateway calls for, with what stood in the way. One entry per VPC: a later
// failure replaces an earlier one, and a later success clears it.
func (h *Handler) noteNetworkProblem(vpcID, netID, detail string) {
	h.netProblems.Store(vpcID, dataplane.VPCNetworkProblem{VpcID: vpcID, NetworkID: netID, Detail: detail})
}

// forgetVPCNetwork is the network side of deleting a VPC: the strategy
// decides what happens to the Docker network, and any problem recorded
// against the VPC goes with the record, so a deleted VPC cannot keep an
// advisory alive.
func (h *Handler) forgetVPCNetwork(ctx context.Context, vpc *VPC) {
	if vpc == nil {
		return
	}
	h.netProblems.Delete(vpc.VpcID)
	h.netProblems.Delete(unbackedProblemKey(vpc.VpcID))
	// Read before OnDelete: the strategy may clear the record's network fields
	// on its way out, and this is the last moment the name is knowable.
	name, netID := h.vpcNetworkLockKey(vpc), vpc.DockerNetworkID
	if vpc.IsDefault || name == h.cfg.Network || netID == h.cfg.Network {
		name = "" // the shared data plane outlives every VPC on it
	}
	h.forgetVPCEgress(ctx, vpc)
	if h.vpcStrategy != nil {
		h.vpcStrategy.OnDelete(ctx, vpc)
	}
	h.forgetVPCNetworkStatus(ctx, name, netID)
}

// forgetVPCNetworkStatus drops name from /_overcast/health once the strategy
// has decided what happens to the network, and only when the network is
// actually gone.
//
// The check is what makes this safe under the `shared` strategy, where several
// VPCs name one bridge: deleting one of them removes a record, not necessarily
// a network, and forgetting on the record alone would take a live network out
// of the report the moment any sharer was deleted.
//
// It asks by id rather than by name, because the id is what the deleted record
// pointed at. Under `shared` the survivors keep that same id, so the question
// "is the thing this record named still here" is answered exactly.
//
// A daemon that cannot be asked forgets anyway. There is nothing to report
// while Docker is down, and forgetting is the direction that cannot invent a
// problem — the next probe re-establishes whatever is true.
func (h *Handler) forgetVPCNetworkStatus(ctx context.Context, name, netID string) {
	if name == "" {
		return
	}
	if h.dockerReady.Load() && h.docker != nil && netID != "" {
		if info, err := h.docker.InspectNetwork(ctx, netID); err == nil && info != nil {
			return
		}
	}
	h.forgetNetworkStatus(name)
}

// networkProblems returns the recorded problems, ordered by VPC ID.
func (h *Handler) networkProblems() []dataplane.VPCNetworkProblem {
	var out []dataplane.VPCNetworkProblem
	h.netProblems.Range(func(_, v any) bool {
		out = append(out, v.(dataplane.VPCNetworkProblem))
		return true
	})
	// By VPC, then by detail: one VPC can carry both a plane problem and an
	// egress one (egressProblemKey), and sync.Map ranges in no order — an
	// advisory that reordered itself between two health polls reads as two
	// different faults.
	sort.Slice(out, func(i, j int) bool {
		if out[i].VpcID != out[j].VpcID {
			return out[i].VpcID < out[j].VpcID
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// vpcNetworkFix names the command that actually resolves this network's drift.
//
// Not always `overcast network reset`. That command reads the internet-gateway
// fact off the network itself (docker.LabelGatewayAttached) and declines every
// network that does not carry one, because a VPC network from before the label
// existed gives it no way to tell an isolated bridge from a gateway-attached
// one — and rebuilding on a guess would write a state nothing chose. So for
// exactly the population an upgrade produces, naming that command sends an
// operator to a refusal (#1584). The repair for those is the startup reconcile,
// which has the store to ask.
func vpcNetworkFix(info *docker.NetworkInspect) string {
	if info == nil {
		return ""
	}
	if _, recorded := info.Labels[docker.LabelGatewayAttached]; !recorded {
		return "restart Overcast — its startup reconcile rebuilds this network; " +
			"`overcast network reset` cannot, because the network predates the recorded gateway state"
	}
	return "overcast network reset " + info.Name
}
