package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// GlueS3Tables returns the glue-s3tables-catalog group: a table bucket read
// through Glue's s3tablescatalog federated catalog, whose child catalogs are
// the table buckets, their databases the namespaces and their tables the
// bucket's Iceberg tables.
//
// Every call is signed, S3 Tables' because its paths are also legal S3 bucket
// names, and Glue's so both read the same Region.
func GlueS3Tables() ServiceGroup {
	g := &glueS3TablesCliGroup{}
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

type glueS3TablesCliGroup struct{}

const glueS3TablesCliTable = "orders"

// glueS3TablesBucket is the run's table bucket: lowercase letters, digits
// and hyphens only.
func glueS3TablesBucket(runID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(runID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	name := "glue-s3tables-" + strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// glueS3TablesNamespace: namespace names allow underscores, not hyphens.
func glueS3TablesNamespace(runID string) string {
	return strings.ReplaceAll(glueS3TablesBucket(runID), "-", "_")
}

func (g *glueS3TablesCliGroup) catalogID(t *harness.TestContext) (string, error) {
	id := t.GetString("glue_s3tables_catalog_id")
	if id == "" {
		return "", fmt.Errorf("no catalog ID from GetCatalogs")
	}
	return id, nil
}

func (g *glueS3TablesCliGroup) setup(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "s3tables", "create-table-bucket", "--name", glueS3TablesBucket(t.RunID))
	if err != nil {
		return err
	}
	arn, _ := out["arn"].(string)
	t.Set("glue_s3tables_bucket_arn", arn)
	ns := glueS3TablesNamespace(t.RunID)
	if err := awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "create-namespace", "--table-bucket-arn", arn, "--namespace", ns); err != nil {
		return err
	}
	return awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "create-table",
		"--table-bucket-arn", arn, "--namespace", ns, "--name", glueS3TablesCliTable, "--format", "ICEBERG",
		"--metadata", `{"iceberg":{"schema":{"fields":[{"name":"id","type":"long","required":true}]}}}`)
}

func (g *glueS3TablesCliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("glue_s3tables_bucket_arn")
	if arn == "" {
		return nil
	}
	ns := glueS3TablesNamespace(t.RunID)
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table", "--table-bucket-arn", arn, "--namespace", ns, "--name", glueS3TablesCliTable) //nolint:errcheck
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-namespace", "--table-bucket-arn", arn, "--namespace", ns)                             //nolint:errcheck
	awscli.RunSigned(t.Endpoint, t.Region, "s3tables", "delete-table-bucket", "--table-bucket-arn", arn)                                             //nolint:errcheck
	return nil
}

func (g *glueS3TablesCliGroup) GetCatalogs(_ context.Context, t *harness.TestContext) error {
	bucket := glueS3TablesBucket(t.RunID)
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "glue", "get-catalogs", "--parent-catalog-id", "s3tablescatalog")
	if err != nil {
		return err
	}
	list, _ := out["CatalogList"].([]any)
	for _, item := range list {
		c, _ := item.(map[string]any)
		if c["Name"] != bucket {
			continue
		}
		id, _ := c["CatalogId"].(string)
		if !strings.HasSuffix(id, ":s3tablescatalog/"+bucket) {
			return fmt.Errorf("GetCatalogs: CatalogId %q", id)
		}
		t.Set("glue_s3tables_catalog_id", id)
		return nil
	}
	return fmt.Errorf("GetCatalogs: %q not listed under s3tablescatalog in %v", bucket, out)
}

func (g *glueS3TablesCliGroup) GetCatalog(_ context.Context, t *harness.TestContext) error {
	id, err := g.catalogID(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "glue", "get-catalog", "--catalog-id", id)
	if err != nil {
		return err
	}
	c, _ := out["Catalog"].(map[string]any)
	federated, _ := c["FederatedCatalog"].(map[string]any)
	if c["Name"] != glueS3TablesBucket(t.RunID) || federated["ConnectionName"] != "aws:s3tables" ||
		federated["Identifier"] != t.GetString("glue_s3tables_bucket_arn") {
		return fmt.Errorf("GetCatalog: got %v", out)
	}
	return nil
}

func (g *glueS3TablesCliGroup) GetDatabases(_ context.Context, t *harness.TestContext) error {
	id, err := g.catalogID(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "glue", "get-databases", "--catalog-id", id)
	if err != nil {
		return err
	}
	list, _ := out["DatabaseList"].([]any)
	if len(list) != 1 {
		return fmt.Errorf("GetDatabases: got %v", out)
	}
	if db, _ := list[0].(map[string]any); db["Name"] != glueS3TablesNamespace(t.RunID) {
		return fmt.Errorf("GetDatabases: got %v", out)
	}
	return nil
}

func (g *glueS3TablesCliGroup) GetTable(_ context.Context, t *harness.TestContext) error {
	id, err := g.catalogID(t)
	if err != nil {
		return err
	}
	out, err := awscli.RunOutputSigned(t.Endpoint, t.Region, "glue", "get-table",
		"--catalog-id", id, "--database-name", glueS3TablesNamespace(t.RunID), "--name", glueS3TablesCliTable)
	if err != nil {
		return err
	}
	tbl, _ := out["Table"].(map[string]any)
	params, _ := tbl["Parameters"].(map[string]any)
	tableType, _ := params["table_type"].(string)
	if location, _ := params["metadata_location"].(string); !strings.EqualFold(tableType, "ICEBERG") || location == "" {
		return fmt.Errorf("GetTable: Parameters %v", params)
	}
	return nil
}
