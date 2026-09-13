package eks

import (
	"context"
	"net"
	"net/url"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// The cluster endpoint, and why it is minted per caller.
//
// A live cluster's endpoint is a *data-plane* address: it points at the k3s
// container Overcast started, not at Overcast. AWS's shape is
// `https://{hash}.{xx}.{region}.eks.amazonaws.com`; Overcast keys the name on
// the cluster name, which is what a caller actually has, under the base the
// caller reached Overcast on — the rule every other data-plane endpoint
// follows (docs/networking/data-plane-endpoints.md, rds/endpoint.go).
//
// The record keeps one canonical form, `https://{ExternalHostname}:{hostPort}`,
// and which of the two dialable answers a caller gets is decided when the
// record is read:
//
//   - A sibling container resolves the endpoint name to the k3s container
//     through Docker's embedded resolver — the alias set clusterEndpointAliases
//     registers — and talks to it on 6443, the port the API server listens on
//     inside the container. Before this was per caller it was handed the host
//     form, and inside any container that hostname resolves to Overcast, which
//     serves nothing on the published port.
//   - The host reaches the same container only through its published port
//     binding, which is the canonical form as stored.

// clusterEndpointHostname builds the DNS name for a cluster's API server on
// base.
func clusterEndpointHostname(name, region, base string) string {
	return name + "." + region + ".eks." + base
}

// endpointBase returns the hostname an endpoint name should be minted under
// for this caller: the configured OVERCAST_HOSTNAME when set, else the host
// they reached Overcast on. An IP-literal origin falls back to the configured
// external hostname, because a name cannot be a subdomain of an address.
func (s *Service) endpointBase(ctx context.Context) string {
	base := serviceutil.ClientBaseURLFromOrigin(s.cfg, middleware.ClientEndpointFromContext(ctx))
	if u, err := url.Parse(base); err == nil {
		if host := u.Hostname(); host != "" && net.ParseIP(host) == nil {
			return host
		}
	}
	return s.cfg.ExternalHostname()
}

// clusterEndpointFor returns the endpoint URL this caller should dial for
// cluster, from the canonical form the record holds.
//
// A record with no endpoint yet, or a mock-mode record, is returned as
// stored: there is nothing dialable to offer in place of the first, and a
// mock endpoint is the marker that decides mixed-mode refusals
// (isMockModeClusterRecord), so it must round-trip untouched.
func (s *Service) clusterEndpointFor(ctx context.Context, region string, cluster *Cluster) string {
	if cluster == nil {
		return ""
	}
	if cluster.Endpoint == "" || s.isMockModeClusterRecord(cluster) ||
		!serviceutil.CallerIsSiblingContainer(middleware.ClientAddrFromContext(ctx)) {
		return cluster.Endpoint
	}
	return "https://" + net.JoinHostPort(clusterEndpointHostname(cluster.Name, region, s.endpointBase(ctx)), k3sAPIPort)
}

// renderCluster returns cluster as this caller should see it: a copy with the
// endpoint minted for them. The record itself is never rewritten with one
// caller's view — see clusterEndpointFor.
func (s *Service) renderCluster(ctx context.Context, region string, cluster *Cluster) *Cluster {
	if cluster == nil || cluster.Endpoint == "" {
		return cluster
	}
	rendered := *cluster
	rendered.Endpoint = s.clusterEndpointFor(ctx, region, cluster)
	return &rendered
}
