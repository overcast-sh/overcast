package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// GlueS3Tables returns the glue-s3tables-catalog group: a table bucket read
// through Glue's s3tablescatalog federated catalog, whose child catalogs are
// the table buckets, their databases the namespaces and their tables the
// bucket's Iceberg tables.
func GlueS3Tables(c *clients.Clients) ServiceGroup {
	g := &glueS3TablesGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"glue-s3tables-catalog:GetCatalogs":  g.GetCatalogs,
			"glue-s3tables-catalog:GetCatalog":   g.GetCatalog,
			"glue-s3tables-catalog:GetDatabases": g.GetDatabases,
			"glue-s3tables-catalog:GetTable":     g.GetTable,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"glue-s3tables-catalog": g.setup,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"glue-s3tables-catalog": g.teardown,
		},
	}
}

type glueS3TablesGroup struct{ c *clients.Clients }

const glueS3TablesTable = "orders"

// bucket is the run's table bucket.
func (g *glueS3TablesGroup) bucket(t *harness.TestContext) string {
	return tableBucketName("glue-s3tables-", t)
}

// tableBucketName is a run's table bucket name under prefix: lowercase
// letters, digits and hyphens, at most 63 of them.
func tableBucketName(prefix string, t *harness.TestContext) string {
	var b strings.Builder
	for _, r := range strings.ToLower(t.RunID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	name := prefix + strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// namespace allows underscores, not hyphens.
func (g *glueS3TablesGroup) namespace(t *harness.TestContext) string {
	return strings.ReplaceAll(g.bucket(t), "-", "_")
}

// catalogID is the bucket's catalog ID, as GetCatalogs reported it.
func (g *glueS3TablesGroup) catalogID(t *harness.TestContext) (string, error) {
	id := t.GetString("glueS3TablesCatalogID")
	if id == "" {
		return "", fmt.Errorf("no catalog ID from GetCatalogs")
	}
	return id, nil
}

func (g *glueS3TablesGroup) setup(ctx context.Context, t *harness.TestContext) error {
	b, err := g.c.S3Tables().CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String(g.bucket(t))})
	if err != nil {
		return err
	}
	t.Set("glueS3TablesBucketARN", aws.ToString(b.Arn))
	if _, err := g.c.S3Tables().CreateNamespace(ctx, &s3tables.CreateNamespaceInput{TableBucketARN: b.Arn, Namespace: []string{g.namespace(t)}}); err != nil {
		return err
	}
	_, err = g.c.S3Tables().CreateTable(ctx, &s3tables.CreateTableInput{
		TableBucketARN: b.Arn, Namespace: aws.String(g.namespace(t)), Name: aws.String(glueS3TablesTable), Format: types.OpenTableFormatIceberg,
		Metadata: &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{Schema: &types.IcebergSchema{Fields: []types.SchemaField{
			{Name: aws.String("id"), Type: aws.String("long"), Required: true},
		}}}},
	})
	return err
}

func (g *glueS3TablesGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	arn := aws.String(t.GetString("glueS3TablesBucketARN"))
	if aws.ToString(arn) == "" {
		return nil
	}
	ns := aws.String(g.namespace(t))
	g.c.S3Tables().DeleteTable(ctx, &s3tables.DeleteTableInput{TableBucketARN: arn, Namespace: ns, Name: aws.String(glueS3TablesTable)}) //nolint:errcheck
	g.c.S3Tables().DeleteNamespace(ctx, &s3tables.DeleteNamespaceInput{TableBucketARN: arn, Namespace: ns})                              //nolint:errcheck
	g.c.S3Tables().DeleteTableBucket(ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: arn})                                         //nolint:errcheck
	return nil
}

func (g *glueS3TablesGroup) GetCatalogs(ctx context.Context, t *harness.TestContext) error {
	out, err := g.c.Glue().GetCatalogs(ctx, &glue.GetCatalogsInput{ParentCatalogId: aws.String("s3tablescatalog")})
	if err != nil {
		return err
	}
	for _, c := range out.CatalogList {
		if aws.ToString(c.Name) == g.bucket(t) {
			id := aws.ToString(c.CatalogId)
			if !strings.HasSuffix(id, ":s3tablescatalog/"+g.bucket(t)) {
				return fmt.Errorf("GetCatalogs: CatalogId %q", id)
			}
			t.Set("glueS3TablesCatalogID", id)
			return nil
		}
	}
	return fmt.Errorf("GetCatalogs: %q not listed under s3tablescatalog", g.bucket(t))
}

func (g *glueS3TablesGroup) GetCatalog(ctx context.Context, t *harness.TestContext) error {
	id, err := g.catalogID(t)
	if err != nil {
		return err
	}
	out, err := g.c.Glue().GetCatalog(ctx, &glue.GetCatalogInput{CatalogId: aws.String(id)})
	if err != nil {
		return err
	}
	c := out.Catalog
	if aws.ToString(c.Name) != g.bucket(t) || c.FederatedCatalog == nil ||
		aws.ToString(c.FederatedCatalog.ConnectionName) != "aws:s3tables" ||
		aws.ToString(c.FederatedCatalog.Identifier) != t.GetString("glueS3TablesBucketARN") {
		return fmt.Errorf("GetCatalog: got %+v", c)
	}
	return nil
}

func (g *glueS3TablesGroup) GetDatabases(ctx context.Context, t *harness.TestContext) error {
	id, err := g.catalogID(t)
	if err != nil {
		return err
	}
	out, err := g.c.Glue().GetDatabases(ctx, &glue.GetDatabasesInput{CatalogId: aws.String(id)})
	if err != nil {
		return err
	}
	if len(out.DatabaseList) != 1 || aws.ToString(out.DatabaseList[0].Name) != g.namespace(t) {
		return fmt.Errorf("GetDatabases: got %+v", out.DatabaseList)
	}
	return nil
}

func (g *glueS3TablesGroup) GetTable(ctx context.Context, t *harness.TestContext) error {
	id, err := g.catalogID(t)
	if err != nil {
		return err
	}
	out, err := g.c.Glue().GetTable(ctx, &glue.GetTableInput{
		CatalogId: aws.String(id), DatabaseName: aws.String(g.namespace(t)), Name: aws.String(glueS3TablesTable),
	})
	if err != nil {
		return err
	}
	tbl := out.Table
	if !strings.EqualFold(tbl.Parameters["table_type"], "ICEBERG") || tbl.Parameters["metadata_location"] == "" {
		return fmt.Errorf("GetTable: Parameters %v", tbl.Parameters)
	}
	return nil
}
