package lambdaweb

// topology.go — Lambda Web Functions' part of the system map: a node per web
// function, listing its endpoints and the revisions each serves, with an edge to
// the S3 bucket the function's newest revision is built from. The function's
// revisions and endpoints are folded into the one node, as a table bucket's
// tables are: drawing each would bury the function that owns them.

import (
	"context"
	"sort"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

// ContributeTopology puts every web function, in every region, on the system
// map. It implements topology.Contributor.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	fns, err := serviceutil.ScanRegions[webFunction](ctx, s.store, nsFunctions, s.cfg.Region)
	if err != nil {
		return err
	}
	revs, err := serviceutil.ScanRegions[revision](ctx, s.store, nsRevisions, s.cfg.Region)
	if err != nil {
		return err
	}
	eps, err := serviceutil.ScanRegions[endpoint](ctx, s.store, nsEndpoints, s.cfg.Region)
	if err != nil {
		return err
	}
	type key struct{ region, function string }
	revsOf := map[key][]*revision{}
	for _, r := range revs {
		k := key{r.Region, r.Value.Function}
		revsOf[k] = append(revsOf[k], r.Value)
	}
	epsOf := map[key][]*endpoint{}
	for _, e := range eps {
		k := key{e.Region, e.Value.Function}
		epsOf[k] = append(epsOf[k], e.Value)
	}

	for _, rf := range fns {
		region, fn := rf.Region, rf.Value
		if fn.Name == "" {
			continue
		}
		k := key{region, fn.Name}
		fnRevs, fnEps := revsOf[k], epsOf[k]
		sort.Slice(fnRevs, func(i, j int) bool { return fnRevs[i].Seq < fnRevs[j].Seq })
		sort.Slice(fnEps, func(i, j int) bool { return fnEps[i].Name < fnEps[j].Name })

		count := len(fnRevs)
		node := topology.Node{
			ID:           topology.NodeID(region, serviceName, fn.Name),
			Service:      serviceName,
			Label:        fn.Name,
			Region:       region,
			WebRevisions: &count,
		}
		// A CloudFormation stack owns the node through any of the three
		// resource types that make up the function.
		anchors := []topology.Ref{topology.CFN(region, cfnWebFunction, fn.ARN)}
		for _, e := range fnEps {
			serves := make([]topology.WebRevisionWeight, 0, len(e.Weights))
			for _, w := range e.Weights {
				serves = append(serves, topology.WebRevisionWeight{RevisionID: w.RevisionID, Weight: w.Weight})
			}
			node.WebEndpoints = append(node.WebEndpoints, topology.WebEndpoint{
				Name: e.Name, Type: e.Type, AuthType: e.AuthType, AutoDeployment: e.AutoDeploy,
				DomainName: e.DomainName, Serves: serves, Environments: s.liveEnvironments(envRegion(e, region), fn.Name, e.Name),
			})
			anchors = append(anchors, topology.CFN(region, cfnWebEndpoint, e.ARN))
		}
		for _, r := range fnRevs {
			anchors = append(anchors, topology.CFN(region, cfnWebRevision, r.ARN))
		}
		g.AddNode(node, anchors...)

		// The newest revision's package is where the code the function runs
		// is stored.
		if len(fnRevs) > 0 {
			if bucket := fnRevs[len(fnRevs)-1].BuildConfig.CodeConfig.S3Object.Bucket; bucket != "" {
				g.AddLink(topology.Link{
					Source:   topology.ID(region, serviceName, fn.Name),
					Target:   topology.ID(region, "s3", bucket),
					IDPrefix: "code",
					Type:     "code",
					Label:    "code",
				})
			}
		}
	}
	return nil
}

// The CloudFormation resource types that make up a web function. They are
// repeated from the cloudformation package, which this one cannot import.
const (
	cfnWebFunction = "AWS::Lambda::WebFunction"
	cfnWebRevision = "AWS::Lambda::WebFunctionRevision"
	cfnWebEndpoint = "AWS::Lambda::WebFunctionEndpoint"
)

// envRegion is the Region whose environments a topology node counts for e: a
// HomeRegion endpoint's own, since the same function name can exist in several
// Regions; every Region for MultiRegion and PerRegion endpoints, which are
// served from more than one.
func envRegion(e *endpoint, nodeRegion string) string {
	if e.Type == endpointHomeRegion {
		return nodeRegion
	}
	return ""
}
