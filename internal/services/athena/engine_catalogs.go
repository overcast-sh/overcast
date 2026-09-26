package athena

import (
	"context"
	"maps"
	"slices"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/services/s3tables"
)

// engine_catalogs.go — the catalogs the engine queries, each created with
// Trino's CREATE CATALOG (catalog.management=dynamic) rather than a file, so
// that one can be added to a running engine.
//
// The two Glue catalogs are created as the engine starts. S3 Tables gets one
// Iceberg REST catalog per table bucket, created before a query runs for
// each bucket the engine has no catalog for yet, and dropped once its bucket
// is gone, so a bucket created or deleted while the engine runs needs no
// restart. Each is named "s3tablescatalog/<bucket>", exactly as Athena names
// it: Trino takes any quoted identifier as a catalog name, so a query that
// names one, or runs in one, reaches it as written.
//
// The REST catalog is S3 Tables' own at /iceberg, signed with SigV4 for
// s3tables, as a client of AWS's endpoint signs it. The gateway lets that
// through only for the key it minted (engine_gateway.go).

// engineCatalog is one catalog the engine is given.
type engineCatalog struct {
	name       string
	connector  string
	properties map[string]string
}

// glueCatalogs are AwsDataCatalog's two catalogs: Hive on the Glue
// metastore, redirecting Iceberg tables to Iceberg on the same catalog.
func glueCatalogs(s engineSettings) []engineCatalog {
	hive := s.awsProperties()
	maps.Copy(hive, map[string]string{
		"hive.metastore":                         "glue",
		"hive.iceberg-catalog-name":              icebergCatalog,
		"hive.non-managed-table-writes-enabled":  "true",
		"hive.non-managed-table-creates-enabled": "true",
	})
	iceberg := s.awsProperties()
	iceberg["iceberg.catalog.type"] = "glue"
	return []engineCatalog{
		{name: hiveCatalog, connector: "hive", properties: hive},
		{name: icebergCatalog, connector: "iceberg", properties: iceberg},
	}
}

// s3TablesCatalog is a table bucket's catalog: Iceberg on S3 Tables' REST
// catalog, whose warehouse is the bucket's ARN. The catalog serves no view
// endpoints, so Trino is told not to ask for views.
func s3TablesCatalog(s engineSettings, bucket events.S3TableBucket) engineCatalog {
	props := s.s3Properties()
	maps.Copy(props, map[string]string{
		"iceberg.catalog.type":                        "rest",
		"iceberg.rest-catalog.uri":                    s.Overcast + s3tables.IcebergRoot,
		"iceberg.rest-catalog.warehouse":              bucket.ARN,
		"iceberg.rest-catalog.security":               "SIGV4",
		"iceberg.rest-catalog.signing-name":           "s3tables",
		"iceberg.rest-catalog.view-endpoints-enabled": "false",
	})
	return engineCatalog{name: s3TablesCatalogName(bucket.Name), connector: "iceberg", properties: props}
}

// s3TablesCatalogName is the catalog a table bucket is queried as, in
// Athena and in the engine alike.
func s3TablesCatalogName(bucket string) string {
	return glue.S3TablesCatalogName + "/" + bucket
}

// createCatalogSQL is the statement that gives the engine c.
func createCatalogSQL(c engineCatalog) string {
	props := make([]string, 0, len(c.properties))
	for _, k := range slices.Sorted(maps.Keys(c.properties)) {
		props = append(props, quoteIdent(k)+" = "+quoteString(c.properties[k]))
	}
	return "CREATE CATALOG IF NOT EXISTS " + quoteIdent(c.name) + " USING " + c.connector +
		" WITH (" + strings.Join(props, ", ") + ")"
}

// dropCatalogSQL is the statement that takes a catalog away.
func dropCatalogSQL(name string) string { return "DROP CATALOG IF EXISTS " + quoteIdent(name) }

// createCatalogs gives a starting engine its catalogs; it fails at the first
// the engine refuses.
func (m *engineManager) createCatalogs(ctx context.Context, endpoint string, catalogs []engineCatalog) error {
	for _, c := range catalogs {
		if err := m.client.run(ctx, endpoint, createCatalogSQL(c)); err != nil {
			return err
		}
	}
	return nil
}

// syncS3TablesCatalogs gives the engine at endpoint a catalog for each table
// bucket it has none for, and drops those of buckets that are gone. A
// catalog the engine refuses is logged and tried again by the next query, so
// that only the queries that name it fail.
func (m *engineManager) syncS3TablesCatalogs(ctx context.Context, endpoint string) {
	b := m.bootAt(endpoint)
	if b == nil || m.tables == nil {
		return
	}
	buckets, err := m.tables.ListTableBuckets(ctx)
	if err != nil {
		m.log.Warn("could not list table buckets for the query engine", zap.Error(err))
		return
	}
	b.catalogsMu.Lock()
	defer b.catalogsMu.Unlock()
	current := make(map[string]bool, len(buckets))
	for _, bucket := range buckets {
		current[bucket.Name] = true
		if !b.tableBuckets[bucket.Name] && m.runCatalogStatement(ctx, endpoint, bucket.Name, createCatalogSQL(s3TablesCatalog(b.settings, bucket))) {
			b.tableBuckets[bucket.Name] = true
		}
	}
	for name := range b.tableBuckets {
		if !current[name] && m.runCatalogStatement(ctx, endpoint, name, dropCatalogSQL(s3TablesCatalogName(name))) {
			delete(b.tableBuckets, name)
		}
	}
}

// runCatalogStatement runs a table bucket's CREATE or DROP CATALOG, and
// reports whether it ran.
func (m *engineManager) runCatalogStatement(ctx context.Context, endpoint, bucket, sql string) bool {
	if err := m.client.run(ctx, endpoint, sql); err != nil {
		m.log.Warn("could not sync a table bucket's catalog on the query engine", zap.String("bucket", bucket), zap.Error(err))
		return false
	}
	return true
}
