package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/document"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"

	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// S3TablesSchemaV2 returns the s3tables-schemav2 group: CreateTable with a
// nested schemaV2 — a list, a map and a struct as Iceberg type documents —
// partitioned by a field of the struct, then the metadata file it wrote.
func S3TablesSchemaV2(c *clients.Clients) ServiceGroup {
	g := &s3tablesSchemaV2Group{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"s3tables-schemav2:CreateTable":              g.CreateTable,
			"s3tables-schemav2:GetTableMetadataLocation": g.GetTableMetadataLocation,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"s3tables-schemav2": g.setup,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"s3tables-schemav2": g.teardown,
		},
	}
}

type s3tablesSchemaV2Group struct{ c *clients.Clients }

const s3tablesSchemaV2Table = "events"

func (g *s3tablesSchemaV2Group) cl() *s3tables.Client { return g.c.S3Tables() }

func (g *s3tablesSchemaV2Group) bucket(t *harness.TestContext) string {
	return tableBucketName("s3tables-v2-", t)
}

// namespace allows underscores, not hyphens.
func (g *s3tablesSchemaV2Group) namespace(t *harness.TestContext) string {
	return strings.ReplaceAll(g.bucket(t), "-", "_")
}

func (g *s3tablesSchemaV2Group) table(t *harness.TestContext) (*string, *string, *string) {
	return aws.String(t.GetString("s3tablesV2BucketARN")), aws.String(g.namespace(t)), aws.String(s3tablesSchemaV2Table)
}

func (g *s3tablesSchemaV2Group) setup(ctx context.Context, t *harness.TestContext) error {
	b, err := g.cl().CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String(g.bucket(t))})
	if err != nil {
		return err
	}
	t.Set("s3tablesV2BucketARN", aws.ToString(b.Arn))
	_, err = g.cl().CreateNamespace(ctx, &s3tables.CreateNamespaceInput{TableBucketARN: b.Arn, Namespace: []string{g.namespace(t)}})
	return err
}

func (g *s3tablesSchemaV2Group) teardown(ctx context.Context, t *harness.TestContext) error {
	arn, ns, name := g.table(t)
	if aws.ToString(arn) == "" {
		return nil
	}
	g.cl().DeleteTable(ctx, &s3tables.DeleteTableInput{TableBucketARN: arn, Namespace: ns, Name: name}) //nolint:errcheck
	g.cl().DeleteNamespace(ctx, &s3tables.DeleteNamespaceInput{TableBucketARN: arn, Namespace: ns})     //nolint:errcheck
	g.cl().DeleteTableBucket(ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: arn})                //nolint:errcheck
	return nil
}

func (g *s3tablesSchemaV2Group) CreateTable(ctx context.Context, t *harness.TestContext) error {
	arn, ns, name := g.table(t)
	out, err := g.cl().CreateTable(ctx, &s3tables.CreateTableInput{
		TableBucketARN: arn, Namespace: ns, Name: name, Format: types.OpenTableFormatIceberg,
		Metadata: &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{
			SchemaV2: &types.IcebergSchemaV2{
				Type: types.SchemaV2FieldTypeStruct, IdentifierFieldIds: []int32{1},
				Fields: []types.SchemaV2Field{
					{Id: aws.Int32(1), Name: aws.String("id"), Required: aws.Bool(true), Type: document.NewLazyDocument("long")},
					{Id: aws.Int32(2), Name: aws.String("tags"), Required: aws.Bool(false), Type: document.NewLazyDocument(map[string]any{
						"type": "list", "element-id": 5, "element": "string", "element-required": false,
					})},
					{Id: aws.Int32(3), Name: aws.String("attributes"), Required: aws.Bool(false), Type: document.NewLazyDocument(map[string]any{
						"type": "map", "key-id": 6, "key": "string", "value-id": 7, "value": "string", "value-required": false,
					})},
					{Id: aws.Int32(4), Name: aws.String("customer"), Required: aws.Bool(true), Type: document.NewLazyDocument(map[string]any{
						"type": "struct", "fields": []any{
							map[string]any{"id": 8, "name": "region", "required": true, "type": "string"},
						},
					})},
				},
			},
			PartitionSpec: &types.IcebergPartitionSpec{Fields: []types.IcebergPartitionField{
				{SourceId: aws.Int32(8), Transform: aws.String("identity"), Name: aws.String("region")},
			}},
		}},
	})
	if err != nil {
		return err
	}
	if !strings.Contains(aws.ToString(out.TableARN), "/table/") || aws.ToString(out.VersionToken) == "" {
		return fmt.Errorf("CreateTable: tableARN %q, versionToken %q", aws.ToString(out.TableARN), aws.ToString(out.VersionToken))
	}
	return nil
}

func (g *s3tablesSchemaV2Group) GetTableMetadataLocation(ctx context.Context, t *harness.TestContext) error {
	arn, ns, name := g.table(t)
	out, err := g.cl().GetTableMetadataLocation(ctx, &s3tables.GetTableMetadataLocationInput{TableBucketARN: arn, Namespace: ns, Name: name})
	if err != nil {
		return err
	}
	loc, warehouse := aws.ToString(out.MetadataLocation), aws.ToString(out.WarehouseLocation)
	if warehouse == "" || !strings.HasPrefix(loc, warehouse+"/metadata/") || !strings.HasSuffix(loc, ".metadata.json") {
		return fmt.Errorf("GetTableMetadataLocation: metadataLocation %q, warehouseLocation %q", loc, warehouse)
	}
	return nil
}
