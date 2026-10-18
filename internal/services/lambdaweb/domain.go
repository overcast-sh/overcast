package lambdaweb

// domain.go — endpoint domain names as the API returns them.
//
// AWS returns {domain-id}.{shard-id}.lambda-web.{region}.on.aws, a name that
// resolves to AWS. Overcast cannot serve on.aws, so, as CloudFront does for
// {id}.cloudfront.net, it returns the same id under the host the caller
// reached Overcast on: {domain-id}.{shard-id}.lambda-web.{region}.{host}. That
// name routes back here through the "lambda-web" host-route label. The stored
// record keeps the AWS form; only its id part is the routing key (domainID).
// The port is not part of a domain name; a client adds it.

import (
	"context"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// mintDomain renders a stored domain on the caller's host. region is the
// Region segment ("global" for a MultiRegion endpoint's single domain).
func (s *Service) mintDomain(ctx context.Context, stored, region string) string {
	if stored == "" {
		return ""
	}
	base := serviceutil.ClientBaseURLFromOrigin(s.cfg, middleware.ClientEndpointFromContext(ctx))
	if host := serviceutil.HostRoutedHostnameFromBase(base, hostLabel, domainID(stored), region); host != "" {
		return host
	}
	return stored
}

// domainOf is the endpoint's top-level domainName.
func (s *Service) domainOf(ctx context.Context, ep *endpoint) string {
	if ep.Type == endpointMultiRegion {
		return s.mintDomain(ctx, ep.DomainName, "global")
	}
	return s.mintDomain(ctx, ep.DomainName, ep.Region)
}

// regionDomainOf is a PerRegion or HomeRegion endpoint's domain in one Region.
func (s *Service) regionDomainOf(ctx context.Context, ep *endpoint, region string) string {
	return s.mintDomain(ctx, ep.RegionDomain[region], region)
}
