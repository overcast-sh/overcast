package elasticache

import (
	"context"
	"net"
	"net/url"

	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Endpoint addresses, and why they are minted per caller.
//
// A cache endpoint is a *data-plane* name: it points at the engine container
// Overcast started, not at Overcast. The record stores the AWS-shaped hostname
// it was created with, and every response re-mints it for whoever is asking,
// on the same terms RDS uses (internal/services/rds/endpoint.go, and
// docs/networking/data-plane-endpoints.md):
//
//   - A sibling container — a Lambda function, an ECS task — is given the
//     hostname under the base its own request arrived on, and the engine's
//     own port. The name resolves through Docker's embedded resolver to the
//     engine container, which listens on 6379 or 11211 inside the network.
//   - The host is given the published port, since the engine port is not
//     bound there, and a loopback address in place of the name when the base
//     carries no wildcard DNS (`*.localhost` does not resolve on Windows).
//
// This used to be decided once, after the container started, from Overcast's
// own vantage: the record was overwritten with the address *Overcast* dials —
// the container's IP beside it, `127.0.0.1` and the host port natively — and
// every caller got that. An address is dialable by exactly one party. The
// container IP means nothing on the host, and `127.0.0.1` names the caller
// itself inside a sibling container, which is how a function discovering its
// cache through DescribeCacheClusters at runtime came to connect to itself.
// The dial target now lives on the record as DialAddress/DialPort, for the
// health check only.

// endpointBase returns the hostname endpoint names should be minted under for
// this caller: the configured OVERCAST_HOSTNAME when set, else the host they
// reached Overcast on. An IP-literal origin falls back to the configured
// external hostname, because a name cannot be a subdomain of an address.
func (h *Handler) endpointBase(ctx context.Context) string {
	base := serviceutil.ClientBaseURLFromOrigin(h.cfg, middleware.ClientEndpointFromContext(ctx))
	if u, err := url.Parse(base); err == nil {
		if host := u.Hostname(); host != "" && net.ParseIP(host) == nil {
			return host
		}
	}
	return h.cfg.ExternalHostname()
}

// loopbackAddress stands in for an endpoint name a host caller cannot resolve.
const loopbackAddress = "127.0.0.1"

// callerDialTarget decides how this caller reaches an engine container: the
// port to dial, and whether the hostname has to give way to a loopback address
// because the caller cannot resolve it. declaredPort is the answer when there
// is no container to reach — the AWS-shaped one, which is right when nothing
// dialable can be offered instead.
//
// **Deviation: AWS always reports the engine port here, and a host caller is
// given the published one instead.** The engine listens on its own port inside
// the network and that port is not bound on the host — only the published
// mapping is (ELASTICACHE_PORT_BASE, 63790 upwards) — so the AWS-shaped answer
// would be a port nothing accepts a connection on.
func (h *Handler) callerDialTarget(ctx context.Context, containerID, engine string, hostPort, declaredPort int) (port int, loopback bool) {
	if serviceutil.CallerIsSiblingContainer(middleware.ClientAddrFromContext(ctx)) {
		if containerID != "" {
			return enginePort(engine), false
		}
		return declaredPort, false
	}
	if hostPort > 0 {
		return hostPort, !serviceutil.SupportsHostRouting("http://" + h.endpointBase(ctx))
	}
	return declaredPort, false
}

// mintEndpoint renders one endpoint for this caller: name is the hostname on
// the caller's base, and the port and address follow callerDialTarget.
func (h *Handler) mintEndpoint(ctx context.Context, name func(base string) string, containerID, engine string, hostPort int) *ClusterEndpoint {
	// The declared port is the engine's, derived rather than read back from the
	// record: a record overwritten by an older build carries a dial target
	// whose port is Overcast's to use and nobody else's.
	port, loopback := h.callerDialTarget(ctx, containerID, engine, hostPort, enginePort(engine))
	if loopback {
		return &ClusterEndpoint{Address: loopbackAddress, Port: port}
	}
	return &ClusterEndpoint{Address: name(h.endpointBase(ctx)), Port: port}
}

// clusterEndpointFor is the ConfigurationEndpoint this caller should be shown
// for c.
func (h *Handler) clusterEndpointFor(ctx context.Context, c *CacheCluster) *ClusterEndpoint {
	if c == nil || c.ConfigurationEndpoint == nil {
		return nil
	}
	return h.mintEndpoint(ctx, func(base string) string {
		return clusterEndpointHostname(c.CacheClusterId, h.region(), base)
	}, c.DockerContainerID, c.Engine, c.HostPort)
}

// replicationGroupEndpointFor is the ConfigurationEndpoint this caller should
// be shown for rg.
func (h *Handler) replicationGroupEndpointFor(ctx context.Context, rg *ReplicationGroup) *ClusterEndpoint {
	if rg == nil || rg.ConfigurationEndpoint == nil {
		return nil
	}
	return h.mintEndpoint(ctx, func(base string) string {
		return replicationGroupEndpointHostname(rg.ReplicationGroupId, h.region(), base)
	}, rg.DockerContainerID, rg.Engine, rg.HostPort)
}

// serverlessEndpointFor is the Endpoint (and ReaderEndpoint — one node, two
// names) this caller should be shown for c.
func (h *Handler) serverlessEndpointFor(ctx context.Context, c *ServerlessCache) *ClusterEndpoint {
	if c == nil || c.Endpoint == nil {
		return nil
	}
	return h.mintEndpoint(ctx, func(base string) string {
		return serverlessEndpointHostname(c.ServerlessCacheName, h.region(), base)
	}, c.DockerContainerID, c.Engine, c.HostPort)
}

// cacheClusterForCaller is c as this caller should see it: a shallow copy with
// the endpoint minted for them. The stored record is never handed to a
// renderer directly, so a caller-specific value cannot leak back into it.
func (h *Handler) cacheClusterForCaller(ctx context.Context, c *CacheCluster) *CacheCluster {
	if c == nil {
		return nil
	}
	view := *c
	view.ConfigurationEndpoint = h.clusterEndpointFor(ctx, c)
	return &view
}

// replicationGroupForCaller is cacheClusterForCaller for a replication group.
func (h *Handler) replicationGroupForCaller(ctx context.Context, rg *ReplicationGroup) *ReplicationGroup {
	if rg == nil {
		return nil
	}
	view := *rg
	view.ConfigurationEndpoint = h.replicationGroupEndpointFor(ctx, rg)
	return &view
}

// serverlessCacheForCaller is cacheClusterForCaller for a serverless cache.
func (h *Handler) serverlessCacheForCaller(ctx context.Context, c *ServerlessCache) *ServerlessCache {
	if c == nil {
		return nil
	}
	view := *c
	endpoint := h.serverlessEndpointFor(ctx, c)
	view.Endpoint = endpoint
	view.ReaderEndpoint = endpoint
	return &view
}

// containerDialTarget is how Overcast itself reaches an engine container: its
// address on the plane Overcast shares with it when Overcast runs beside it,
// else the published port on loopback. For health checks only — see the file
// comment for why this is not the endpoint.
func (h *Handler) containerDialTarget(ctx context.Context, containerID, engine string, hostPort int) (string, int) {
	if addr := dataplane.ContainerAddr(ctx, h.docker, h.cfg, containerID); addr != "" {
		return addr, enginePort(engine)
	}
	return loopbackAddress, hostPort
}

// dialTarget is where the health check connects. A record written by an
// older build has no dial fields and carries the target in its endpoint
// instead, which is what that build meant by it.
func dialTarget(dialAddress string, dialPort int, endpoint *ClusterEndpoint) (string, int) {
	if dialAddress != "" {
		return dialAddress, dialPort
	}
	if endpoint != nil {
		return endpoint.Address, endpoint.Port
	}
	return "", 0
}

func (c *CacheCluster) dialTarget() (string, int) {
	return dialTarget(c.DialAddress, c.DialPort, c.ConfigurationEndpoint)
}

func (rg *ReplicationGroup) dialTarget() (string, int) {
	return dialTarget(rg.DialAddress, rg.DialPort, rg.ConfigurationEndpoint)
}

func (c *ServerlessCache) dialTarget() (string, int) {
	return dialTarget(c.DialAddress, c.DialPort, c.Endpoint)
}
