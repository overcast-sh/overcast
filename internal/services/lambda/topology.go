package lambda

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

// ContributeTopology puts every function, in every region, on the system map
// with edges to its log group and, for an image function, its ECR repository;
// and draws every event source mapping from its SQS queue or DynamoDB table.
// It implements topology.Contributor.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	fns, err := serviceutil.ScanRegions[Function](ctx, s.store, nsFunctions, s.cfg.Region)
	if err != nil {
		return err
	}
	for _, rf := range fns {
		region, fn := rf.Region, rf.Value
		self := topology.ID(region, "lambda", fn.Name)
		g.AddNode(topology.Node{
			ID:      topology.NodeID(region, "lambda", fn.Name),
			Service: "lambda",
			Label:   fn.Name,
			Region:  region,
		}, topology.CFN(region, "AWS::Lambda::Function", fn.Name))

		logGroup := fn.LogGroup
		if logGroup == "" {
			logGroup = "/aws/lambda/" + fn.Name
		}
		g.AddLink(topology.Link{
			Source:   self,
			Target:   topology.ID(region, "logs", logGroup).AnyRegion(),
			IDPrefix: "logs",
			Type:     "logs",
		})
		if strings.EqualFold(fn.PackageType, "Image") {
			g.AddLink(topology.Link{
				Source:   topology.Image(fn.ImageUri),
				Target:   self,
				IDPrefix: "ecr-lambda",
				Type:     "container-image",
			})
		}
	}

	mappings, err := serviceutil.ScanRegions[EventSourceMapping](ctx, s.store, nsESM, s.cfg.Region)
	if err != nil {
		return err
	}
	for _, rm := range mappings {
		contributeESM(g, rm.Region, rm.Value)
	}
	return nil
}

// contributeESM draws one event source mapping. Its ARNs can name a stale
// region (a mapping imported from another stack), so both endpoints fall back
// to the same queue, table or function in another region. A DynamoDB mapping
// with filter criteria gets a filter node between the table and the function.
func contributeESM(g *topology.Graph, region string, m *EventSourceMapping) {
	kind := esmSourceType(m.EventSourceArn) // also the source's node kind
	if kind != "sqs" && kind != "dynamodb" {
		return
	}
	arnRegion := func(arn string) string {
		if r := serviceutil.ARNRegion(arn); r != "" {
			return r
		}
		return region
	}
	srcRegion, fnName := arnRegion(m.EventSourceArn), functionNameFromARN(m.FunctionArn)
	src := topology.ID(srcRegion, kind, esmSourceName(m.EventSourceArn)).AnyRegion()
	fn := topology.ID(arnRegion(m.FunctionArn), "lambda", fnName).AnyRegion()

	patterns := filterPatterns(m.FilterCriteria)
	if kind != "dynamodb" || len(patterns) == 0 {
		g.AddLink(topology.Link{Source: src, Target: fn, IDPrefix: "esm", Type: "esm"})
		return
	}
	g.AddAnchoredNode(topology.Node{
		ID:             topology.NodeID(srcRegion, "esm-filter", m.UUID),
		Service:        "esm-filter",
		Label:          "ESM filter",
		Region:         srcRegion,
		ESMID:          m.UUID,
		FunctionName:   fnName,
		EventSource:    esmSourceName(m.EventSourceArn),
		SourceType:     kind,
		FilterPatterns: patterns,
	}, src, fn)
	filter := topology.ID(srcRegion, "esm-filter", m.UUID)
	g.AddLink(topology.Link{Source: src, Target: filter, ID: "esm-filter-in::" + m.UUID, Type: "esm-filter", Label: "filter"})
	g.AddLink(topology.Link{Source: filter, Target: fn, ID: "esm-filter-out::" + m.UUID, Type: "esm", Label: "matched"})
}

func filterPatterns(fc *FilterCriteria) []string {
	if fc == nil {
		return nil
	}
	var out []string
	for _, f := range fc.Filters {
		if f.Pattern != "" {
			out = append(out, f.Pattern)
		}
	}
	return out
}
