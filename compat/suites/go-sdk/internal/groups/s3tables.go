package groups

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"
	smithy "github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// S3Tables returns the S3 Tables service group.
func S3Tables(c *clients.Clients) ServiceGroup {
	g := &s3tablesGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"s3tables-tables:CreateTableBucket":                     g.CreateTableBucket,
			"s3tables-tables:GetTableBucket":                        g.GetTableBucket,
			"s3tables-tables:ListTableBuckets":                      g.ListTableBuckets,
			"s3tables-tables:CreateNamespace":                       g.CreateNamespace,
			"s3tables-tables:ListNamespaces":                        g.ListNamespaces,
			"s3tables-tables:CreateTable":                           g.CreateTable,
			"s3tables-tables:GetTable":                              g.GetTable,
			"s3tables-tables:ListTables":                            g.ListTables,
			"s3tables-tables:UpdateTableMetadataLocation":           g.UpdateTableMetadataLocation,
			"s3tables-tables:UpdateTableMetadataLocationStaleToken": g.UpdateTableMetadataLocationStaleToken,
			"s3tables-tables:DeleteTable":                           g.DeleteTable,
			"s3tables-tables:DeleteNamespace":                       g.DeleteNamespace,
			"s3tables-tables:DeleteTableBucket":                     g.DeleteTableBucket,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"s3tables-tables": g.teardown,
		},
	}
}

type s3tablesGroup struct{ c *clients.Clients }

func (g *s3tablesGroup) cl() *s3tables.Client { return g.c.S3Tables() }

const s3tablesTableName = "orders"

// bucketName is the run's table bucket: lowercase letters, digits and hyphens.
func (g *s3tablesGroup) bucketName(t *harness.TestContext) string {
	var b strings.Builder
	for _, r := range strings.ToLower(t.RunID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	name := "s3tables-tables-" + strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// namespace is the run's namespace: namespace names allow underscores, not
// hyphens.
func (g *s3tablesGroup) namespace(t *harness.TestContext) string {
	return strings.ReplaceAll(g.bucketName(t), "-", "_")
}

func (g *s3tablesGroup) bucketARN(t *harness.TestContext) (string, error) {
	arn := t.GetString("s3tablesBucketARN")
	if arn == "" {
		return "", fmt.Errorf("no table bucket from CreateTableBucket")
	}
	return arn, nil
}

func (g *s3tablesGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	arn := t.GetString("s3tablesBucketARN")
	if arn == "" {
		return nil
	}
	ns := g.namespace(t)
	g.cl().DeleteTable(ctx, &s3tables.DeleteTableInput{ //nolint:errcheck
		TableBucketARN: aws.String(arn), Namespace: aws.String(ns), Name: aws.String(s3tablesTableName),
	})
	g.cl().DeleteNamespace(ctx, &s3tables.DeleteNamespaceInput{ //nolint:errcheck
		TableBucketARN: aws.String(arn), Namespace: aws.String(ns),
	})
	g.cl().DeleteTableBucket(ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: aws.String(arn)}) //nolint:errcheck
	return nil
}

func (g *s3tablesGroup) CreateTableBucket(ctx context.Context, t *harness.TestContext) error {
	name := g.bucketName(t)
	out, err := g.cl().CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String(name)})
	if err != nil {
		return err
	}
	arn := aws.ToString(out.Arn)
	if !strings.HasPrefix(arn, "arn:aws:s3tables:") || !strings.HasSuffix(arn, ":bucket/"+name) {
		return fmt.Errorf("CreateTableBucket: unexpected ARN %q", arn)
	}
	t.Set("s3tablesBucketARN", arn)
	return nil
}

func (g *s3tablesGroup) GetTableBucket(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().GetTableBucket(ctx, &s3tables.GetTableBucketInput{TableBucketARN: aws.String(arn)})
	if err != nil {
		return err
	}
	if aws.ToString(out.Arn) != arn || aws.ToString(out.Name) != g.bucketName(t) {
		return fmt.Errorf("GetTableBucket: got %q / %q", aws.ToString(out.Arn), aws.ToString(out.Name))
	}
	if out.CreatedAt == nil || aws.ToString(out.OwnerAccountId) == "" {
		return fmt.Errorf("GetTableBucket: missing createdAt or ownerAccountId")
	}
	return nil
}

func (g *s3tablesGroup) ListTableBuckets(ctx context.Context, t *harness.TestContext) error {
	name := g.bucketName(t)
	out, err := g.cl().ListTableBuckets(ctx, &s3tables.ListTableBucketsInput{Prefix: aws.String(name)})
	if err != nil {
		return err
	}
	for _, b := range out.TableBuckets {
		if aws.ToString(b.Name) == name {
			return nil
		}
	}
	return fmt.Errorf("ListTableBuckets: %q not listed", name)
}

func (g *s3tablesGroup) CreateNamespace(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	ns := g.namespace(t)
	out, err := g.cl().CreateNamespace(ctx, &s3tables.CreateNamespaceInput{TableBucketARN: aws.String(arn), Namespace: []string{ns}})
	if err != nil {
		return err
	}
	if len(out.Namespace) != 1 || out.Namespace[0] != ns || aws.ToString(out.TableBucketARN) != arn {
		return fmt.Errorf("CreateNamespace: got %v in %q", out.Namespace, aws.ToString(out.TableBucketARN))
	}
	return nil
}

func (g *s3tablesGroup) ListNamespaces(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().ListNamespaces(ctx, &s3tables.ListNamespacesInput{TableBucketARN: aws.String(arn)})
	if err != nil {
		return err
	}
	if len(out.Namespaces) != 1 || out.Namespaces[0].Namespace[0] != g.namespace(t) {
		return fmt.Errorf("ListNamespaces: expected [%s], got %+v", g.namespace(t), out.Namespaces)
	}
	return nil
}

func (g *s3tablesGroup) CreateTable(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().CreateTable(ctx, &s3tables.CreateTableInput{
		TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t)),
		Name: aws.String(s3tablesTableName), Format: types.OpenTableFormatIceberg,
	})
	if err != nil {
		return err
	}
	if !strings.HasPrefix(aws.ToString(out.TableARN), arn+"/table/") || aws.ToString(out.VersionToken) == "" {
		return fmt.Errorf("CreateTable: got %q / %q", aws.ToString(out.TableARN), aws.ToString(out.VersionToken))
	}
	t.Set("s3tablesTableARN", aws.ToString(out.TableARN))
	t.Set("s3tablesVersionToken", aws.ToString(out.VersionToken))
	return nil
}

func (g *s3tablesGroup) GetTable(ctx context.Context, t *harness.TestContext) error {
	tableARN := t.GetString("s3tablesTableARN")
	if tableARN == "" {
		return fmt.Errorf("GetTable: no table from CreateTable")
	}
	out, err := g.cl().GetTable(ctx, &s3tables.GetTableInput{TableArn: aws.String(tableARN)})
	if err != nil {
		return err
	}
	if aws.ToString(out.Name) != s3tablesTableName || out.Format != types.OpenTableFormatIceberg {
		return fmt.Errorf("GetTable: got name %q format %q", aws.ToString(out.Name), out.Format)
	}
	warehouse := aws.ToString(out.WarehouseLocation)
	if !strings.HasPrefix(warehouse, "s3://") {
		return fmt.Errorf("GetTable: warehouseLocation %q is not an s3:// URI", warehouse)
	}
	t.Set("s3tablesWarehouse", warehouse)
	return nil
}

func (g *s3tablesGroup) ListTables(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	out, err := g.cl().ListTables(ctx, &s3tables.ListTablesInput{TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t))})
	if err != nil {
		return err
	}
	if len(out.Tables) != 1 || aws.ToString(out.Tables[0].Name) != s3tablesTableName {
		return fmt.Errorf("ListTables: expected [%s], got %+v", s3tablesTableName, out.Tables)
	}
	return nil
}

func (g *s3tablesGroup) metadataLocation(ctx context.Context, t *harness.TestContext, arn string) (*s3tables.GetTableMetadataLocationOutput, error) {
	return g.cl().GetTableMetadataLocation(ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t)), Name: aws.String(s3tablesTableName),
	})
}

func (g *s3tablesGroup) UpdateTableMetadataLocation(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	cur, err := g.metadataLocation(ctx, t, arn)
	if err != nil {
		return err
	}
	loc := aws.ToString(cur.WarehouseLocation) + "/metadata/00001-compat.metadata.json"
	out, err := g.cl().UpdateTableMetadataLocation(ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t)), Name: aws.String(s3tablesTableName),
		VersionToken: cur.VersionToken, MetadataLocation: aws.String(loc),
	})
	if err != nil {
		return err
	}
	if aws.ToString(out.MetadataLocation) != loc || aws.ToString(out.VersionToken) == aws.ToString(cur.VersionToken) {
		return fmt.Errorf("UpdateTableMetadataLocation: location %q token %q (was %q)",
			aws.ToString(out.MetadataLocation), aws.ToString(out.VersionToken), aws.ToString(cur.VersionToken))
	}
	after, err := g.metadataLocation(ctx, t, arn)
	if err != nil {
		return err
	}
	if aws.ToString(after.MetadataLocation) != loc {
		return fmt.Errorf("GetTableMetadataLocation after update: %q", aws.ToString(after.MetadataLocation))
	}
	t.Set("s3tablesStaleToken", aws.ToString(cur.VersionToken))
	return nil
}

func (g *s3tablesGroup) UpdateTableMetadataLocationStaleToken(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	stale := t.GetString("s3tablesStaleToken")
	cur, err := g.metadataLocation(ctx, t, arn)
	if err != nil {
		return err
	}
	_, err = g.cl().UpdateTableMetadataLocation(ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t)), Name: aws.String(s3tablesTableName),
		VersionToken: aws.String(stale), MetadataLocation: aws.String(aws.ToString(cur.WarehouseLocation) + "/metadata/00002-compat.metadata.json"),
	})
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ConflictException" {
		return fmt.Errorf("UpdateTableMetadataLocation with a stale token: expected ConflictException, got %v", err)
	}
	return nil
}

func (g *s3tablesGroup) DeleteTable(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	if _, err := g.cl().DeleteTable(ctx, &s3tables.DeleteTableInput{
		TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t)), Name: aws.String(s3tablesTableName),
	}); err != nil {
		return err
	}
	_, err = g.cl().GetTable(ctx, &s3tables.GetTableInput{TableArn: aws.String(t.GetString("s3tablesTableARN"))})
	var nf *types.NotFoundException
	if !errors.As(err, &nf) {
		return fmt.Errorf("GetTable after DeleteTable: expected NotFoundException, got %v", err)
	}
	return nil
}

func (g *s3tablesGroup) DeleteNamespace(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	_, err = g.cl().DeleteNamespace(ctx, &s3tables.DeleteNamespaceInput{TableBucketARN: aws.String(arn), Namespace: aws.String(g.namespace(t))})
	return err
}

func (g *s3tablesGroup) DeleteTableBucket(ctx context.Context, t *harness.TestContext) error {
	arn, err := g.bucketARN(t)
	if err != nil {
		return err
	}
	if _, err := g.cl().DeleteTableBucket(ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: aws.String(arn)}); err != nil {
		return err
	}
	_, err = g.cl().GetTableBucket(ctx, &s3tables.GetTableBucketInput{TableBucketARN: aws.String(arn)})
	var nf *types.NotFoundException
	if !errors.As(err, &nf) {
		return fmt.Errorf("GetTableBucket after DeleteTableBucket: expected NotFoundException, got %v", err)
	}
	return nil
}
