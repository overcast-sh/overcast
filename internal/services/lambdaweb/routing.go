package lambdaweb

// routing.go — the routing table the data plane resolves a request's host
// against. It is an immutable snapshot of every endpoint (with its function,
// the revisions it serves and the function's resource policy), rebuilt when the
// control plane changes, so a request costs two atomic loads and a map lookup —
// no store read, no JSON decode.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// hostLabel is the host-route label web function endpoints are addressed by,
// {domain-id}.{shard-id}.lambda-web.{region}.{base}. It is the same string
// middleware.LabelLambdaWeb registers.
const hostLabel = "lambda-web"

// domainID is the part of an endpoint domain before ".lambda-web.": the two
// labels {domain-id}.{shard-id}, or {domain-id} for a global MultiRegion name.
// It is the routing key: stored domains are in AWS's form, minted ones are on
// the caller's host, and only this part is common to both.
func domainID(domain string) string {
	if i := strings.Index(domain, "."+hostLabel+"."); i > 0 {
		return strings.ToLower(domain[:i])
	}
	return strings.ToLower(domain)
}

// route is one resolved endpoint domain.
type route struct {
	// id is the routing key (see domainID).
	id string
	// servedRegion is the Region this domain's environments run in.
	servedRegion string
	function     *webFunction
	endpoint     *endpoint
	endpointARN  string
	policy       *policyRecord
	// revs are the revisions the endpoint serves, with cumulative weights.
	revs        []routeRev
	totalWeight int
	// rt is the endpoint's runtime state (pools, limiter); it outlives
	// snapshots.
	rt *endpointRT
}

// routeRev is one entry of an endpoint's revision weights.
type routeRev struct {
	rev    *revision
	weight int
	// pool is the (endpoint, revision) environment pool, resolved once per
	// snapshot so the request path takes no lock to find it.
	pool *pool
	// cumulative is the sum of weights up to and including this entry.
	cumulative int
}

// active reports whether the endpoint accepts traffic. A record written before
// the data plane has no state and is active.
func (r *route) active() bool {
	return r.endpoint.State == "" || r.endpoint.State == stateActive
}

// revisionActive reports whether a revision can serve traffic.
func revisionActive(rev *revision) bool {
	return rev.State == "" || rev.State == stateActive
}

// routeTable is an immutable snapshot of every route.
type routeTable struct {
	gen    uint64
	byHost map[string]*route
}

// invalidateRoutes tells the data plane the control plane changed: the next
// request rebuilds the table. It is cheap and safe to call from any goroutine.
func (s *Service) invalidateRoutes() { s.dp.routeGen.Add(1) }

// routes returns the current routing table, rebuilding it first if the control
// plane changed since it was built.
func (s *Service) routes(ctx context.Context) (*routeTable, error) {
	if t := s.dp.table.Load(); t != nil && t.gen == s.dp.routeGen.Load() {
		return t, nil
	}
	return s.rebuildRoutes(ctx)
}

func (s *Service) rebuildRoutes(ctx context.Context) (*routeTable, error) {
	s.dp.rebuildMu.Lock()
	defer s.dp.rebuildMu.Unlock()
	gen := s.dp.routeGen.Load()
	if t := s.dp.table.Load(); t != nil && t.gen == gen {
		return t, nil
	}

	fns, err := serviceutil.ScanRegions[webFunction](ctx, s.store, nsFunctions, s.cfg.Region)
	if err != nil {
		return nil, err
	}
	eps, err := serviceutil.ScanRegions[endpoint](ctx, s.store, nsEndpoints, s.cfg.Region)
	if err != nil {
		return nil, err
	}
	revs, err := serviceutil.ScanRegions[revision](ctx, s.store, nsRevisions, s.cfg.Region)
	if err != nil {
		return nil, err
	}
	policies, err := s.store.Scan(ctx, nsPolicies, "")
	if err != nil {
		return nil, err
	}

	type fnKey struct{ region, name string }
	fnByKey := make(map[fnKey]*webFunction, len(fns))
	for _, f := range fns {
		fnByKey[fnKey{f.Region, f.Value.Name}] = f.Value
	}
	revByKey := make(map[[3]string]*revision, len(revs))
	for _, r := range revs {
		revByKey[[3]string{r.Region, r.Value.Function, r.Value.ID}] = r.Value
	}
	policyByARN := make(map[string]*policyRecord, len(policies))
	for _, kv := range policies {
		var p policyRecord
		if json.Unmarshal([]byte(kv.Value), &p) == nil {
			policyByARN[kv.Key] = &p
		}
	}

	t := &routeTable{gen: gen, byHost: make(map[string]*route, len(eps))}
	live := make(map[string]struct{}, len(eps))
	type servingSet struct {
		rt      *endpointRT
		serving map[string]struct{}
	}
	rtServing := make(map[string]*servingSet)
	for _, e := range eps {
		ep := e.Value
		fn := fnByKey[fnKey{e.Region, ep.Function}]
		if fn == nil {
			continue
		}
		domains := map[string]string{} // routing key → served region
		if ep.Type == endpointMultiRegion {
			if ep.DomainName != "" {
				domains[domainID(ep.DomainName)] = e.Region
			}
		} else {
			for region, d := range ep.RegionDomain {
				if d != "" {
					domains[domainID(d)] = region
				}
			}
		}
		for id, region := range domains {
			rt := s.endpointRuntime(region, fn.Name, ep.Name, id)
			live[rt.key] = struct{}{}
			rt.configure(ep)
			r := &route{
				id: id, servedRegion: region, function: fn, endpoint: ep,
				endpointARN: ep.ARN, policy: policyByARN[fn.ARN], rt: rt,
			}
			cum := 0
			for _, w := range ep.Weights {
				rev := revByKey[[3]string{e.Region, fn.Name, w.RevisionID}]
				if rev == nil || w.Weight <= 0 {
					continue
				}
				cum += w.Weight
				r.revs = append(r.revs, routeRev{rev: rev, weight: w.Weight, cumulative: cum, pool: rt.pool(rev.ID)})
				ss := rtServing[rt.key]
				if ss == nil {
					ss = &servingSet{rt: rt, serving: map[string]struct{}{}}
					rtServing[rt.key] = ss
				}
				ss.serving[rev.ID] = struct{}{}
			}
			if rtServing[rt.key] == nil {
				rtServing[rt.key] = &servingSet{rt: rt, serving: map[string]struct{}{}}
			}
			r.totalWeight = cum
			t.byHost[id] = r
		}
	}
	s.retireEndpointRuntimes(live)
	for _, rt := range rtServing {
		rt.rt.retirePools(s, rt.serving)
	}
	s.dp.table.Store(t)
	return t, nil
}
