package s3tables

// topology.go — S3 Tables' part of the system map: a node per table bucket
// listing its tables by namespace, each with its snapshot count and latest
// commit. A table's "--table-s3" warehouse bucket is folded into its table
// bucket's node rather than drawn as an S3 bucket of its own: it is an
// implementation detail of the table, and drawing it would double every one.

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

// ContributeTopology puts every table bucket, in every region, on the system
// map, with a row per table. It implements topology.Contributor.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	buckets, err := serviceutil.ScanRegions[tableBucket](ctx, s.store, nsBuckets, s.cfg.Region)
	if err != nil {
		return err
	}
	tables, err := serviceutil.ScanRegions[tableRecord](ctx, s.store, nsTables, s.cfg.Region)
	if err != nil {
		return err
	}
	rows := make(map[string][]topology.DataTable) // bucket node ID → its tables
	for _, rt := range tables {
		region, t := rt.Region, rt.Value
		if t.TableID == "" {
			continue
		}
		bucket := topology.NodeID(region, serviceName, t.Bucket)
		rows[bucket] = append(rows[bucket], s.tableRow(ctx, t))
		if warehouse, _, ok := serviceutil.SplitS3URI(t.WarehouseLocation); ok {
			g.Absorb(topology.ID(region, "s3", warehouse), topology.ID(region, serviceName, t.Bucket))
		}
	}
	for _, rb := range buckets {
		region, b := rb.Region, rb.Value
		if b.Name == "" {
			continue
		}
		id := topology.NodeID(region, serviceName, b.Name)
		slices.SortFunc(rows[id], func(a, b topology.DataTable) int {
			return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
		})
		g.AddNode(topology.Node{
			ID:      id,
			Service: serviceName,
			Label:   b.Name,
			Region:  region,
			Tables:  rows[id],
		}, topology.CFN(region, "AWS::S3Tables::TableBucket", b.ARN))
	}
	return nil
}

// tableRow is t as its bucket node lists it. Its snapshots are read from its
// current metadata file, which a table that cannot be read goes without.
func (s *Service) tableRow(ctx context.Context, t *tableRecord) topology.DataTable {
	row := topology.DataTable{Name: t.Name, Namespace: t.Namespace, ID: t.TableID, Location: t.WarehouseLocation}
	if sum, ok := s.snapshotSummary(ctx, t.MetadataLocation); ok {
		row.Snapshots, row.LastCommit = &sum.count, sum.current
	}
	return row
}

// snapshotSummary is what the map shows of a metadata file's snapshots.
type snapshotSummary struct {
	count   int
	current *topology.TableCommit
}

// snapshotCache remembers the snapshot summary of each metadata file the map
// has read. A commit points a table at a new file rather than changing the
// one it had, so a location's summary holds, and the map — fetched again on
// every table event — reads each file once instead of every table's each time.
type snapshotCache struct {
	mu      sync.Mutex
	entries map[string]snapshotSummary
}

// snapshotCacheSize bounds the cache: past it, the cache starts again.
const snapshotCacheSize = 1024

// snapshotSummary is what the map shows of the snapshots in the metadata
// file at location; ok is false when there is none or it cannot be read. A
// failed read is not remembered, so a file that appears later is read then.
func (s *Service) snapshotSummary(ctx context.Context, location string) (snapshotSummary, bool) {
	if location == "" || s.getObject == nil {
		return snapshotSummary{}, false
	}
	if sum, ok := s.snapshots.load(location); ok {
		return sum, true
	}
	meta, aerr := s.readMetadata(ctx, location)
	if aerr != nil {
		return snapshotSummary{}, false
	}
	sum := snapshotSummary{count: len(meta.Snapshots)}
	if sn, ok := meta.CurrentSnapshot(); ok {
		sum.current = &topology.TableCommit{
			SnapshotID:     strconv.FormatInt(sn.SnapshotID, 10),
			Operation:      sn.Summary[summaryOperation],
			AddedRecords:   summaryCount(sn, summaryAddedRecords),
			DeletedRecords: summaryCount(sn, summaryDeletedRecords),
			CommittedAt:    sn.TimestampMS,
		}
	}
	s.snapshots.store(location, sum)
	return sum, true
}

func (c *snapshotCache) load(location string) (snapshotSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sum, ok := c.entries[location]
	return sum, ok
}

func (c *snapshotCache) store(location string, sum snapshotSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= snapshotCacheSize {
		c.entries = make(map[string]snapshotSummary)
	}
	c.entries[location] = sum
}
