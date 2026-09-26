package glue

// topology.go — the Data Catalog's part of the system map: a node per
// database listing its tables, an edge from a database to each bucket its
// tables' data is in, and s3tablescatalog with an edge to each table bucket
// it federates.

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

// catalogNodeKind is the node-ID kind of s3tablescatalog's node, which keeps
// it apart from a database that happens to have the same name.
const catalogNodeKind = "glue-catalog"

// ContributeTopology implements topology.Contributor. The Data Catalog is not
// partitioned by region here, so every node is in the default region.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	databases, err := s.store.listDatabases(ctx)
	if err != nil {
		return err
	}
	partitions, err := s.store.partitionCounts(ctx)
	if err != nil {
		return err
	}
	region := s.cfg.Region
	for _, db := range databases {
		tables, err := s.store.listTables(ctx, db.Name)
		if err != nil {
			return err
		}
		s.contributeDatabase(g, region, db.Name, tables, partitions)
	}
	s.contributeFederation(ctx, g, region)
	return nil
}

// contributeDatabase adds a database's node, with a row per table, and an
// edge to each bucket its tables' data is in.
func (s *Service) contributeDatabase(g *topology.Graph, region, name string, tables []*tableRecord, partitions map[string]int) {
	rows := make([]topology.DataTable, len(tables))
	var buckets []string
	for i, t := range tables {
		count := partitions[tableKey(t.DatabaseName, t.Name)]
		rows[i] = topology.DataTable{
			Name:       t.Name,
			Format:     tableFormat(&t.Table),
			Location:   storageLocation(&t.Table),
			Partitions: &count,
		}
		if bucket, _, ok := serviceutil.SplitS3URI(dataLocation(&t.Table)); ok && !slices.Contains(buckets, bucket) {
			buckets = append(buckets, bucket)
		}
	}
	node := topology.ID(region, serviceName, name)
	g.AddNode(topology.Node{
		ID:               topology.NodeID(region, serviceName, name),
		Service:          serviceName,
		Label:            name,
		Region:           region,
		GlueResourceType: topology.GlueDatabase,
		Tables:           rows,
	}, topology.CFN(region, "AWS::Glue::Database", name))
	for _, bucket := range buckets {
		g.AddLink(topology.Link{
			Source: node, Target: topology.ID(region, "s3", bucket).AnyRegion(),
			IDPrefix: "table-location", Type: "table-location",
		})
	}
}

// contributeFederation adds s3tablescatalog, with an edge to each table
// bucket it mounts, once there is a bucket for it to mount. A bucket list
// that fails leaves the catalog off the map, not the databases with it.
func (s *Service) contributeFederation(ctx context.Context, g *topology.Graph, region string) {
	if s.s3tables == nil {
		return
	}
	buckets, err := s.s3tables.ListTableBuckets(ctx)
	if err != nil {
		s.log.Warn("topology: s3tablescatalog left off the map", zap.Error(err))
		return
	}
	if len(buckets) == 0 {
		return
	}
	g.AddNode(topology.Node{
		ID:               topology.NodeID(region, catalogNodeKind, S3TablesCatalogName),
		Service:          serviceName,
		Label:            S3TablesCatalogName,
		Region:           region,
		GlueResourceType: topology.GlueCatalog,
	})
	catalog := topology.ID(region, catalogNodeKind, S3TablesCatalogName)
	for _, b := range buckets {
		g.AddLink(topology.Link{
			Source: catalog, Target: topology.ID(region, "s3tables", b.Name).AnyRegion(),
			IDPrefix: "federation", Type: "federation",
		})
	}
}

// ─── Table format and location ───────────────────────────────────

// Table formats, as the console's format badge names them.
const (
	formatIceberg = "ICEBERG"
	formatView    = "VIEW"
)

// formatMarkers are matched against a table's SerDe, then its input format,
// then its classification, the way Athena and a crawler read a table.
var formatMarkers = []struct {
	format string
	marker *regexp.Regexp
}{
	{"PARQUET", regexp.MustCompile(`(?i)parquet`)},
	// Not a bare "orc": that would match inside other class names.
	{"ORC", regexp.MustCompile(`(?i)^orc$|\.orc\.|orcserde|orcinputformat`)},
	{"AVRO", regexp.MustCompile(`(?i)avro`)},
	{"JSON", regexp.MustCompile(`(?i)json`)},
	{"CSV", regexp.MustCompile(`(?i)csv|lazysimpleserde|textinputformat`)},
}

// tableFormat is what the table holds: ICEBERG, VIEW or a file format, and
// "" when nothing on the table says.
func tableFormat(t *Table) string {
	if strings.EqualFold(tableParameter(t, "table_type"), formatIceberg) {
		return formatIceberg
	}
	if t.TableType == "VIRTUAL_VIEW" {
		return formatView
	}
	var clues []string
	if sd := t.StorageDescriptor; sd != nil {
		if sd.SerdeInfo != nil {
			clues = append(clues, sd.SerdeInfo.SerializationLibrary)
		}
		clues = append(clues, sd.InputFormat)
	}
	clues = append(clues, tableParameter(t, "classification"))
	for _, clue := range clues {
		for _, m := range formatMarkers {
			if clue != "" && m.marker.MatchString(clue) {
				return m.format
			}
		}
	}
	return ""
}

// tableParameter is a table parameter by name, ignoring case: Athena writes
// table_type, PyIceberg TABLE_TYPE.
func tableParameter(t *Table, name string) string {
	for k, v := range t.Parameters {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func storageLocation(t *Table) string {
	if t.StorageDescriptor == nil {
		return ""
	}
	return t.StorageDescriptor.Location
}

// dataLocation is where the table's data is read from: an Iceberg table's
// current metadata file, and any other table's storage location.
func dataLocation(t *Table) string {
	if loc := tableParameter(t, "metadata_location"); loc != "" {
		return loc
	}
	return storageLocation(t)
}
