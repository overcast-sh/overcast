package rds

import (
	"context"
	"net"
	"net/url"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Endpoint addresses, and why they are minted per caller.
//
// An RDS endpoint is a *data-plane* name: it points at the engine container
// Overcast started, not at Overcast. AWS's shape is
// `{id}.{hash}.{region}.rds.amazonaws.com`; Overcast mints
// `{id}.{region}.rds.{base}` — the same grammar with the account-specific hash
// dropped, on the base the caller reached Overcast on. That last part is the
// rule every other service already follows for the URLs it hands back
// (docs/plans/client-facing-url-minting.md, serviceutil.ClientBaseURL): a value
// Overcast returns must be dialable by whoever asked for it, and a caller who
// reached us on `localhost.overcast.sh` cannot resolve a name ending in a
// hostname they have never heard of.
//
// Reachability is Docker's, not ours: the engine container carries the endpoint
// name as a network alias on every network emulated compute runs on, so a
// Lambda function or ECS task resolving it gets the container's address from
// Docker's embedded resolver before the query ever reaches Overcast's. Because
// the name minted depends on the caller, the alias set covers every hostname
// Overcast could mint under — see instanceEndpointAliases and
// docs/dev/container-networking.md.

// instanceEndpointHostname builds the DNS name for a DB instance on base.
func instanceEndpointHostname(id, region, base string) string {
	return id + "." + region + ".rds." + base
}

// The discriminators that separate an Aurora cluster's two endpoints.
//
// AWS spells them `cluster-{hash}` and `cluster-ro-{hash}`.
//
// **Deviation: the hash is dropped**, here and in every other endpoint name
// Overcast mints. It has to be. On AWS the hash is assigned by the service and
// is not derivable from anything the caller knows, so a name carrying one can
// only be *read back* from an API response — which is fine on AWS, where DNS is
// authoritative, and impossible here, where the name must also be registrable
// as a Docker network alias before anyone asks for it, and reproducible by any
// Overcast process that later has to compute the same alias set. Inventing a
// stable per-cluster hash would satisfy neither goal: it would still be a value
// nobody could predict, and it would make the name longer for no gain.
//
// Dropping it leaves exactly the two labels below — `cluster` reduces from
// AWS's `cluster-{hash}`, and stays unambiguous against `cluster-ro`.
//
// Overcast minted `cluster-rw` for the writer until 0.0.1-alpha.37, on the
// reasoning that a bare `cluster` looked too plain to be deliberate. It was a
// label AWS has never used, in the one field whose whole job is to look like
// AWS's.
const (
	clusterRoleWriter = "cluster"
	clusterRoleReader = "cluster-ro"
)

// clusterEndpointHostname builds the DNS name for an Aurora cluster endpoint on
// base. role is the discriminator: clusterRoleWriter or clusterRoleReader.
func clusterEndpointHostname(id, role, region, base string) string {
	return id + "." + role + "." + region + ".rds." + base
}

// endpointBase returns the hostname new endpoint names should be minted under
// for this caller: the configured OVERCAST_HOSTNAME when set, else the host
// they reached Overcast on.
//
// An IP-literal origin falls back to the configured external hostname, because
// a name cannot be a subdomain of an address. Host-side callers are unaffected
// by that fallback — they are handed a loopback address anyway when the base
// carries no wildcard DNS (see instanceEndpointFor).
func (h *Handler) endpointBase(ctx context.Context) string {
	base := serviceutil.ClientBaseURLFromOrigin(h.cfg, middleware.ClientEndpointFromContext(ctx))
	if u, err := url.Parse(base); err == nil {
		if host := u.Hostname(); host != "" && net.ParseIP(host) == nil {
			return host
		}
	}
	return h.externalHostname()
}

// instanceEndpointFor returns the address and port this caller should dial for
// inst.
//
// Both halves are per-caller, for the same reason and with the same precedent
// as the API's own published-vs-listen port split (docs/networking.md § Which
// host and port a URL carries):
//
//   - A sibling container resolves the endpoint name to the engine container
//     through Docker's embedded resolver, and talks to it on the engine's own
//     port — 3306, exactly as on AWS.
//   - The host reaches the same container through its published port binding,
//     and only through that; the engine port is not bound on the host. It is
//     given a loopback address when the base carries no wildcard DNS, since
//     `*.localhost` does not resolve on Windows.
//
// An instance with no container behind it (Docker unavailable, or the engine
// image could not be resolved) keeps the AWS-shaped name and its configured
// port: there is nothing dialable to offer, and a name is a better answer than
// a loopback address that is certainly wrong.
func (h *Handler) instanceEndpointFor(ctx context.Context, inst *DBInstance) (string, int) {
	if inst == nil {
		return "", 0
	}
	base := h.endpointBase(ctx)
	name := instanceEndpointHostname(inst.DBInstanceIdentifier, h.instanceRegion(ctx), base)

	port, loopback := h.callerDialTarget(ctx, inst, base, inst.Port)
	if loopback {
		return loopbackAddress, port
	}
	return name, port
}

// loopbackAddress stands in for an endpoint name a host caller cannot resolve.
const loopbackAddress = "127.0.0.1"

// callerDialTarget decides how this caller reaches inst's engine container: the
// port to dial, and whether the endpoint hostname has to be replaced by a
// loopback address because the caller cannot resolve it.
//
// declaredPort is the port to fall back to when there is no container to reach
// — the instance's own for an instance endpoint, the cluster's for a cluster
// one. Both are the AWS-shaped answer, which is the right one when there is
// nothing dialable to offer instead.
//
// **Deviation: AWS always reports the engine port here, and a host caller is
// given the published one instead.** The constraint is Docker's, not ours. The
// engine listens on 3306/5432 inside the network and that port is not bound on
// the host — only the published mapping is (RDS_PORT_BASE, 33060
// upwards, because 3306 is usually taken by a local install). So the AWS-shaped
// answer is a port nothing accepts a connection on, and the rule this file
// exists to keep (a value Overcast returns must be dialable by whoever asked
// for it) leaves one option. docs/networking.md § Data-plane endpoints carries
// the table, and the one caveat: a host-side deploy that reads Endpoint.Port
// bakes the host port into container environment.
//
// Split out from instanceEndpointFor because a cluster endpoint answers the
// same question about the same container: an Aurora cluster has no container of
// its own, so `orders.cluster…` and `orders-writer…` are two names for one
// engine and must agree on the port behind them. They did not before — the
// cluster endpoint always carried the cluster's engine port, so on the host it
// named a port nothing binds while the instance endpoint named the right one.
func (h *Handler) callerDialTarget(ctx context.Context, inst *DBInstance, base string, declaredPort int) (port int, loopback bool) {
	if inst == nil {
		return declaredPort, false
	}

	if serviceutil.CallerIsSiblingContainer(middleware.ClientAddrFromContext(ctx)) {
		if inst.DockerContainerID != "" {
			if ecfg, ok := engineEnvConfig[inst.Engine]; ok && ecfg.ContainerPort > 0 {
				return ecfg.ContainerPort, false
			}
		}
		return declaredPort, false
	}

	if inst.HostPort > 0 {
		return inst.HostPort, !serviceutil.SupportsHostRouting("http://" + base)
	}
	return declaredPort, false
}

// clusterEndpointsFor returns the writer and reader endpoint addresses for this
// caller, and the port behind both.
//
// A cluster has no container of its own — its writer member's container is what
// the cluster endpoints point at, so the address and port are that instance's,
// rendered on exactly the terms instanceEndpointFor uses. Both names resolve to
// the writer; see clusterAliasesForInstance for why the reader endpoint does
// not spread over the replicas.
func (h *Handler) clusterEndpointsFor(ctx context.Context, c *DBCluster) (writer, reader string, port int) {
	if c == nil {
		return "", "", 0
	}
	base := h.endpointBase(ctx)
	region := h.instanceRegion(ctx)
	writer = clusterEndpointHostname(c.DBClusterIdentifier, clusterRoleWriter, region, base)
	reader = clusterEndpointHostname(c.DBClusterIdentifier, clusterRoleReader, region, base)

	port, loopback := h.callerDialTarget(ctx, h.clusterWriterInstance(ctx, c), base, c.Port)
	if loopback {
		return loopbackAddress, loopbackAddress, port
	}
	return writer, reader, port
}

// clusterWriterInstance returns the member whose container backs c's cluster
// endpoints, or nil when the cluster has no writer on record — a cluster
// created but not yet given an instance, or one whose members predate the
// stored record.
func (h *Handler) clusterWriterInstance(ctx context.Context, c *DBCluster) *DBInstance {
	if c == nil || h.store == nil {
		return nil
	}
	for _, m := range c.DBClusterMembers {
		if !m.IsClusterWriter {
			continue
		}
		inst, aerr := h.store.getDBInstance(ctx, m.DBInstanceIdentifier)
		if aerr != nil {
			return nil
		}
		return inst
	}
	return nil
}

// instanceRegion is the region to render names for: the caller's, which is the
// region the record was read from.
func (h *Handler) instanceRegion(ctx context.Context) string {
	if h.store != nil {
		if region := h.store.region(ctx); region != "" {
			return region
		}
	}
	return h.region()
}

// dialTarget is the address and port *Overcast itself* uses to reach the engine
// container — health checks, not responses. It is deliberately not the endpoint
// a client is given: Overcast may be on the host (where only the published port
// is bound) or in a container beside the engine (where only the engine port is).
func dialTarget(inst *DBInstance) (string, int) {
	if inst == nil {
		return "", 0
	}
	if inst.DialAddress != "" && inst.DialPort > 0 {
		return inst.DialAddress, inst.DialPort
	}
	if inst.Endpoint != nil {
		return inst.Endpoint.Address, inst.Endpoint.Port
	}
	return "", 0
}

// instanceEndpointAliases is the set of DNS names the engine container must
// answer to on the networks emulated compute runs on.
//
// It covers the endpoint name under every hostname Overcast could mint it
// under, not only the configured one, because the name a caller holds depends
// on the endpoint *they* used: a stack deployed through `localhost.overcast.sh`
// bakes that name into a task definition, while a Lambda calling
// DescribeDBInstances over the container endpoint is handed the same instance
// under whichever name its own request carried.
//
// Docker aliases are exact-match, so an unregistered name resolves nowhere —
// and under a split-horizon domain it is worse than nowhere: the query falls
// through to Overcast's own resolver, which answers any subdomain of those
// domains with Overcast's address, so the client connects to Overcast on 3306
// and hangs. See docs/dev/container-networking.md for which resolver answers
// what.
//
// The set is bounded (four or five names) and costs one entry each in Docker's
// per-network alias table.
func (h *Handler) instanceEndpointAliases(region string, inst *DBInstance) []string {
	if inst == nil || inst.DBInstanceIdentifier == "" {
		return nil
	}
	if region == "" {
		region = h.region()
	}
	// Whatever the record already advertises goes in too, in case it was
	// minted under a hostname the current configuration no longer lists.
	var advertised []string
	if inst.Endpoint != nil {
		advertised = append(advertised, inst.Endpoint.Address)
	}
	return dataplane.Hostnames(h.cfg, func(base string) string {
		return instanceEndpointHostname(inst.DBInstanceIdentifier, region, base)
	}, advertised...)
}

// containerAliases is every DNS name inst's engine container must answer to on
// the networks emulated compute runs on: its own endpoint, plus its cluster's
// when it is the writer. This is what startDBContainer attaches with, on a
// fresh container and an adopted one alike.
func (h *Handler) containerAliases(ctx context.Context, region string, inst *DBInstance) []string {
	return append(h.instanceEndpointAliases(region, inst),
		h.clusterAliasesForInstance(ctx, region, inst)...)
}

// instancePlacement is where inst's engine container belongs: its subnet
// group's VPC, the default plane when it has none, and — the part that was
// missing — the default plane *as well* when the instance is publicly
// accessible. PubliclyAccessible is AWS's own escape hatch from a VPC, the
// one docs/networking/vpcs.md tells people to reach for, and until this it
// changed DescribeDBInstances and nothing else: the container joined its VPC
// network alone, and a function outside that VPC had its endpoint refused.
//
// Every attach goes through here — fresh, adopted, promoted — so the record
// and the wiring cannot disagree about it.
func (h *Handler) instancePlacement(ctx context.Context, inst *DBInstance, aliases []string) (dataplane.Placement, error) {
	placement, err := dataplane.PlaceInSubnets(ctx, h.vpcResolver, inst.VpcID, h.instanceSubnets(ctx, inst))
	if err != nil {
		return placement, err
	}
	placement.Public = inst.PubliclyAccessibleOrDefault()
	placement.Aliases = aliases
	return placement, nil
}

// reattachInstance moves inst's running container to the placement its
// record now calls for, after a modification changed it. Reattach leaves
// every plane and rejoins, which drops open connections — as it does on AWS,
// where a PubliclyAccessible change is applied with a network interruption.
// Best-effort, and logged rather than surfaced: the record is already
// committed, and the next restart applies the same placement anyway.
func (h *Handler) reattachInstance(ctx context.Context, inst *DBInstance) {
	if h.docker == nil || !h.dockerReady.Load() || inst == nil || inst.DockerContainerID == "" {
		return
	}
	placement, err := h.instancePlacement(ctx, inst, h.containerAliases(ctx, h.store.region(ctx), inst))
	if err != nil {
		h.log.Warn("RDS: modified instance could not be placed in its VPC — "+
			"its reachability is unchanged until it is restarted",
			zap.String("instance", inst.DBInstanceIdentifier), zap.Error(err))
		return
	}
	if err := dataplane.Reattach(ctx, h.docker, h.cfg, inst.DockerContainerID, placement); err != nil {
		h.log.Warn("RDS: modified instance could not be re-attached — "+
			"its reachability is unchanged until it is restarted",
			zap.String("instance", inst.DBInstanceIdentifier), zap.Error(err))
	}
}

// instanceSubnets returns the subnets an instance's DB subnet group names, or
// nil for an instance without one (which lands in the default VPC) or one
// whose group Overcast has no record of. Under OVERCAST_VPC_EGRESS=routed the
// subnets' route tables decide whether the engine gets a route out.
func (h *Handler) instanceSubnets(ctx context.Context, inst *DBInstance) []string {
	if inst == nil || inst.DBSubnetGroupName == "" {
		return nil
	}
	sg, aerr := h.store.getDBSubnetGroup(ctx, inst.DBSubnetGroupName)
	if aerr != nil || sg == nil {
		return nil
	}
	return sg.SubnetIds
}

// adoptClusterEndpoints makes instanceID's engine container answer to the
// cluster endpoints it has just been promoted to serve.
//
// The record is only half of a failover. clusterEndpointsFor renders
// DescribeDBClusters' address and port from whichever member is the writer, so
// the *response* side follows a promotion the moment the member list changes —
// but the name a client actually dials is a Docker alias, and Docker fixes
// those when a container joins a network. Without this the cluster endpoint
// keeps resolving to the container that was just deleted, and the response and
// DNS disagree about where the cluster is.
//
// Nothing strips the names from the outgoing writer. On the path that promotes
// anyone — DeleteDBInstance — its container has already been stopped and queued
// for removal, and a stopped container answers no queries. Detaching it as well
// would mean a second round of Docker calls against a container that is on its
// way out.
//
// Best-effort, and logged rather than surfaced: the promotion itself has
// already been committed to the store, and the caller's response with it.
func (h *Handler) adoptClusterEndpoints(ctx context.Context, instanceID string) {
	if h.docker == nil || !h.dockerReady.Load() {
		return
	}
	inst, aerr := h.store.getDBInstance(ctx, instanceID)
	if aerr != nil || inst == nil || inst.DockerContainerID == "" {
		return
	}

	placement, err := h.instancePlacement(ctx, inst, h.containerAliases(ctx, h.store.region(ctx), inst))
	if err != nil {
		h.log.Warn("RDS: promoted instance could not be placed in its VPC — "+
			"the cluster endpoints still resolve to the old writer",
			zap.String("instance", instanceID), zap.Error(err))
		return
	}

	if err := dataplane.Reattach(ctx, h.docker, h.cfg, inst.DockerContainerID, placement); err != nil {
		h.log.Warn("RDS: promoted instance could not take over the cluster endpoints — "+
			"they will not resolve until it is restarted",
			zap.String("instance", instanceID), zap.Error(err))
	}
}

// clusterAliasesForInstance is the set of Aurora cluster endpoint names inst's
// container must additionally answer to, or nil when inst is not the cluster's
// writer.
//
// The cluster endpoint is the name most consumers actually hold: CDK's
// `rds.DatabaseCluster` exposes `clusterEndpoint`/`clusterReadEndpoint`, and a
// template referencing anything else is the non-idiomatic path. Registering
// only the instance name therefore left the standard one resolving nowhere —
// and, ending in a domain Overcast's own zone claims, not even failing cleanly:
// the query fell through to public DNS, which answers every subdomain of those
// domains with a loopback address. The caller resolved the cluster, dialled
// itself, and got a refused connection.
//
// **Deviation: both names go on the writer.** On AWS the reader endpoint
// load-balances over the Aurora Replicas and falls back to the writer only when
// there are none. The constraint here is that Aurora's shared storage layer is
// not emulated: every member gets its own engine container with its own
// container-local storage (this package mounts no volume and configures no
// replication), so a replica is not a copy of the writer — it is an empty
// database that happens to have the same credentials. A reader endpoint spread
// over the replicas would therefore answer some queries with nothing.
//
// Pointing it at the writer diverges from AWS in how reads are *distributed*;
// pointing it at the replicas would diverge in what they *return*, which is the
// far worse half to be wrong about — and it fails silently, as a query that
// finds no rows. The visible cost of this choice is that replica lag cannot be
// reproduced locally, so a read-after-write race passes here and can still fail
// on AWS; docs/services/rds.md says so where users will see it.
//
// When Aurora storage is really emulated, this is the seam that changes.
func (h *Handler) clusterAliasesForInstance(ctx context.Context, region string, inst *DBInstance) []string {
	if inst == nil || inst.DBClusterIdentifier == "" || h.store == nil {
		return nil
	}
	c, aerr := h.store.getDBCluster(ctx, inst.DBClusterIdentifier)
	if aerr != nil || c == nil {
		return nil
	}
	if !isClusterWriter(c, inst.DBInstanceIdentifier) {
		return nil
	}
	return h.clusterEndpointAliases(region, c)
}

// clusterEndpointAliases is both cluster endpoint names under every hostname
// Overcast could mint them on, for the same reason instanceEndpointAliases
// registers a set rather than the configured name alone.
func (h *Handler) clusterEndpointAliases(region string, c *DBCluster) []string {
	if c == nil || c.DBClusterIdentifier == "" {
		return nil
	}
	if region == "" {
		region = h.region()
	}
	// Whatever the record already advertises goes in too, in case it was
	// minted under a hostname the current configuration no longer lists.
	names := dataplane.Hostnames(h.cfg, func(base string) string {
		return clusterEndpointHostname(c.DBClusterIdentifier, clusterRoleWriter, region, base)
	}, c.Endpoint)
	names = append(names, dataplane.Hostnames(h.cfg, func(base string) string {
		return clusterEndpointHostname(c.DBClusterIdentifier, clusterRoleReader, region, base)
	}, c.ReaderEndpoint)...)
	// Each half is deduplicated against itself; the advertised names can
	// collide across the two, so the union is filtered once more.
	return docker.EndpointAliases(names...)
}

// isClusterWriter reports whether instanceID is — or is about to become — c's
// writer.
//
// The "about to" matters: CreateDBInstance registers cluster membership around
// the same moment it starts the container, so the alias set is often computed
// for an instance the cluster does not list yet. addInstanceToCluster makes the
// first member the writer, and anticipating that is what keeps the race from
// costing the cluster its endpoints entirely.
func isClusterWriter(c *DBCluster, instanceID string) bool {
	writerExists := false
	for _, m := range c.DBClusterMembers {
		if m.DBInstanceIdentifier == instanceID {
			return m.IsClusterWriter
		}
		if m.IsClusterWriter {
			writerExists = true
		}
	}
	return !writerExists
}
