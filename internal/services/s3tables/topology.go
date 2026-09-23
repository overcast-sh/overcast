package s3tables

import (
	"context"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

// ContributeTopology puts every table bucket and table, in every region, on
// the system map, with an edge from each table to its bucket. A table's node
// is keyed by its id, which RenameTable leaves alone, and labelled
// namespace.table. It implements topology.Contributor.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	buckets, err := serviceutil.ScanRegions[tableBucket](ctx, s.store, nsBuckets, s.cfg.Region)
	if err != nil {
		return err
	}
	for _, rb := range buckets {
		region, b := rb.Region, rb.Value
		if b.Name == "" {
			continue
		}
		g.AddNode(topology.Node{
			ID:      topology.NodeID(region, serviceName, b.Name),
			Service: serviceName,
			Label:   b.Name,
			Region:  region,
		}, topology.CFN(region, "AWS::S3Tables::TableBucket", b.ARN))
	}

	tables, err := serviceutil.ScanRegions[tableRecord](ctx, s.store, nsTables, s.cfg.Region)
	if err != nil {
		return err
	}
	for _, rt := range tables {
		region, t := rt.Region, rt.Value
		if t.TableID == "" {
			continue
		}
		id := t.Bucket + "/" + t.TableID
		g.AddNode(topology.Node{
			ID:      topology.NodeID(region, serviceName, id),
			Service: serviceName,
			Label:   t.Namespace + "." + t.Name,
			Region:  region,
		}, topology.CFN(region, "AWS::S3Tables::Table", t.ARN))
		g.AddLink(topology.Link{
			Source:   topology.ID(region, serviceName, id),
			Target:   topology.ID(region, serviceName, t.Bucket),
			IDPrefix: "s3tables-bucket",
			Type:     "s3tables-table",
			Label:    "table",
		})
	}
	return nil
}
