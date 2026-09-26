package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// AthenaS3Tables returns the athena-s3tables group: an Iceberg table created,
// written and read through Athena in a table bucket's catalog,
// "s3tablescatalog/<bucket>", each write moving the table's metadata
// location in S3 Tables.
func AthenaS3Tables(c *clients.Clients) ServiceGroup {
	g := &athenaS3TablesGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"athena-s3tables:CreateTableStatement": g.CreateTableStatement,
			"athena-s3tables:InsertStatement":      g.InsertStatement,
			"athena-s3tables:SelectFromTable":      g.SelectFromTable,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"athena-s3tables": g.setup,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"athena-s3tables": g.teardown,
		},
	}
}

type athenaS3TablesGroup struct{ c *clients.Clients }

const (
	athenaS3TablesNamespace = "sales"
	athenaS3TablesTable     = "orders"
)

func (g *athenaS3TablesGroup) bucket(t *harness.TestContext) string {
	return tableBucketName("athena-s3tables-", t)
}

func (g *athenaS3TablesGroup) catalog(t *harness.TestContext) string {
	return "s3tablescatalog/" + g.bucket(t)
}

// results is the S3 bucket query results are written to.
func (g *athenaS3TablesGroup) results(t *harness.TestContext) string {
	return t.RunID + "-athena-s3tables-results"
}

func (g *athenaS3TablesGroup) setup(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.c.S3().CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(g.results(t))}); err != nil {
		return err
	}
	b, err := g.c.S3Tables().CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String(g.bucket(t))})
	if err != nil {
		return err
	}
	t.Set("athenaS3TablesBucketARN", aws.ToString(b.Arn))
	_, err = g.c.S3Tables().CreateNamespace(ctx, &s3tables.CreateNamespaceInput{TableBucketARN: b.Arn, Namespace: []string{athenaS3TablesNamespace}})
	return err
}

func (g *athenaS3TablesGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	if arn := aws.String(t.GetString("athenaS3TablesBucketARN")); aws.ToString(arn) != "" {
		ns := aws.String(athenaS3TablesNamespace)
		g.c.S3Tables().DeleteTable(ctx, &s3tables.DeleteTableInput{TableBucketARN: arn, Namespace: ns, Name: aws.String(athenaS3TablesTable)}) //nolint:errcheck
		g.c.S3Tables().DeleteNamespace(ctx, &s3tables.DeleteNamespaceInput{TableBucketARN: arn, Namespace: ns})                                //nolint:errcheck
		g.c.S3Tables().DeleteTableBucket(ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: arn})                                           //nolint:errcheck
	}
	results := aws.String(g.results(t))
	if out, err := g.c.S3().ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: results}); err == nil {
		for _, o := range out.Contents {
			g.c.S3().DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: results, Key: o.Key}) //nolint:errcheck
		}
	}
	g.c.S3().DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: results}) //nolint:errcheck
	return nil
}

// run runs query in the table bucket's catalog, in the namespace.
func (g *athenaS3TablesGroup) run(ctx context.Context, t *harness.TestContext, query string) (string, error) {
	return runAthenaQuery(ctx, g.c, &athena.StartQueryExecutionInput{
		QueryString:           aws.String(query),
		QueryExecutionContext: &types.QueryExecutionContext{Catalog: aws.String(g.catalog(t)), Database: aws.String(athenaS3TablesNamespace)},
		ResultConfiguration:   &types.ResultConfiguration{OutputLocation: aws.String("s3://" + g.results(t) + "/")},
	})
}

// metadataLocation is the table's current metadata file, from S3 Tables.
func (g *athenaS3TablesGroup) metadataLocation(ctx context.Context, t *harness.TestContext) (string, error) {
	out, err := g.c.S3Tables().GetTableMetadataLocation(ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(t.GetString("athenaS3TablesBucketARN")),
		Namespace:      aws.String(athenaS3TablesNamespace), Name: aws.String(athenaS3TablesTable),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.MetadataLocation), nil
}

func (g *athenaS3TablesGroup) CreateTableStatement(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.run(ctx, t, "CREATE TABLE "+athenaS3TablesTable+" (id int, amount double) TBLPROPERTIES ('table_type' = 'iceberg')"); err != nil {
		return err
	}
	loc, err := g.metadataLocation(ctx, t)
	if err != nil {
		return err
	}
	if loc == "" {
		return fmt.Errorf("GetTableMetadataLocation: the created table has no metadata location")
	}
	t.Set("athenaS3TablesMetadataLocation", loc)
	return nil
}

func (g *athenaS3TablesGroup) InsertStatement(ctx context.Context, t *harness.TestContext) error {
	id, err := g.run(ctx, t, "INSERT INTO "+athenaS3TablesTable+" VALUES (1, 9.5), (2, 20.0)")
	if err != nil {
		return err
	}
	res, err := g.c.Athena().GetQueryResults(ctx, &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id)})
	if err != nil {
		return err
	}
	if n := aws.ToInt64(res.UpdateCount); n != 2 {
		return fmt.Errorf("GetQueryResults: UpdateCount %d, want 2", n)
	}
	loc, err := g.metadataLocation(ctx, t)
	if err != nil {
		return err
	}
	if before := t.GetString("athenaS3TablesMetadataLocation"); loc == before {
		return fmt.Errorf("GetTableMetadataLocation: still %q after the INSERT", loc)
	}
	return nil
}

func (g *athenaS3TablesGroup) SelectFromTable(ctx context.Context, t *harness.TestContext) error {
	id, err := runAthenaQuery(ctx, g.c, &athena.StartQueryExecutionInput{
		QueryString:         aws.String(`SELECT id, amount FROM "` + g.catalog(t) + `"."` + athenaS3TablesNamespace + `"."` + athenaS3TablesTable + `" ORDER BY id`),
		ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String("s3://" + g.results(t) + "/")},
	})
	if err != nil {
		return err
	}
	res, err := g.c.Athena().GetQueryResults(ctx, &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id)})
	if err != nil {
		return err
	}
	if got := strings.Join(queryRows(res), ";"); got != "id,amount;1,9.5;2,20.0" {
		return fmt.Errorf("GetQueryResults: rows %q, want the header then 1,9.5 and 2,20.0", got)
	}
	return nil
}
