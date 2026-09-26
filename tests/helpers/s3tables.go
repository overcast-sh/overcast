package helpers

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"
)

// SeedS3Table creates table bucket, namespace and an Iceberg table in them
// through the S3 Tables API, the table with an "id long" and an
// "amount decimal(10,2)" column, and returns the bucket's ARN. The services
// that read S3 Tables in-process — Glue's s3tablescatalog, and Athena through
// it — test against what it leaves.
func SeedS3Table(t *testing.T, srv *TestServer, bucket, namespace, table string) string {
	t.Helper()
	ctx := context.Background()
	c := s3tables.New(s3tables.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
	b, err := c.CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String(bucket)})
	if err != nil {
		t.Fatalf("CreateTableBucket %s: %v", bucket, err)
	}
	if _, err := c.CreateNamespace(ctx, &s3tables.CreateNamespaceInput{TableBucketARN: b.Arn, Namespace: []string{namespace}}); err != nil {
		t.Fatalf("CreateNamespace %s: %v", namespace, err)
	}
	if _, err := c.CreateTable(ctx, &s3tables.CreateTableInput{
		TableBucketARN: b.Arn, Namespace: aws.String(namespace), Name: aws.String(table), Format: types.OpenTableFormatIceberg,
		Metadata: &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{Schema: &types.IcebergSchema{Fields: []types.SchemaField{
			{Name: aws.String("id"), Type: aws.String("long"), Required: true},
			{Name: aws.String("amount"), Type: aws.String("decimal(10,2)")},
		}}}},
	}); err != nil {
		t.Fatalf("CreateTable %s: %v", table, err)
	}
	return aws.ToString(b.Arn)
}
