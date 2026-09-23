package s3tables_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/document"
	"github.com/aws/aws-sdk-go-v2/service/s3tables/types"
	smithy "github.com/aws/smithy-go"

	s3tablessvc "github.com/overcast-sh/overcast/internal/services/s3tables"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── Clients ──────────────────────────────────────────────────────────────────

func tablesClient(srv *helpers.TestServer) *s3tables.Client {
	return s3tables.New(s3tables.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

func s3Client(srv *helpers.TestServer) *s3.Client {
	return s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		HTTPClient:   http.DefaultClient,
	})
}

// apiErrorCode returns the modeled error code an SDK call failed with.
func apiErrorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not an API error: %v", err)
	}
	return apiErr.ErrorCode()
}

// fixture is a table bucket with one namespace.
type fixture struct {
	ctx       context.Context
	srv       *helpers.TestServer
	tables    *s3tables.Client
	bucketARN string
	namespace string
}

func newFixture(t *testing.T, bucket string) *fixture {
	t.Helper()
	srv := helpers.NewTestServer(t)
	f := &fixture{ctx: context.Background(), srv: srv, tables: tablesClient(srv), namespace: "analytics"}
	out, err := f.tables.CreateTableBucket(f.ctx, &s3tables.CreateTableBucketInput{Name: aws.String(bucket)})
	if err != nil {
		t.Fatalf("CreateTableBucket: %v", err)
	}
	f.bucketARN = aws.ToString(out.Arn)
	if _, err := f.tables.CreateNamespace(f.ctx, &s3tables.CreateNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: []string{f.namespace},
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	return f
}

func (f *fixture) createTable(t *testing.T, name string, metadata types.TableMetadata) *s3tables.CreateTableOutput {
	t.Helper()
	out, err := f.tables.CreateTable(f.ctx, &s3tables.CreateTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace),
		Name: aws.String(name), Format: types.OpenTableFormatIceberg, Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("CreateTable %s: %v", name, err)
	}
	return out
}

// ─── Table buckets ────────────────────────────────────────────────────────────

func TestTableBucket_lifecycle(t *testing.T) {
	// Given: a table bucket
	f := newFixture(t, "lifecycle-bucket")

	// When: it is read back
	got, err := f.tables.GetTableBucket(f.ctx, &s3tables.GetTableBucketInput{TableBucketARN: aws.String(f.bucketARN)})
	if err != nil {
		t.Fatalf("GetTableBucket: %v", err)
	}

	// Then: it carries the AWS ARN, owner and creation time
	if f.bucketARN != "arn:aws:s3tables:us-east-1:000000000000:bucket/lifecycle-bucket" {
		t.Errorf("ARN = %q", f.bucketARN)
	}
	if aws.ToString(got.Name) != "lifecycle-bucket" || aws.ToString(got.OwnerAccountId) != "000000000000" ||
		got.CreatedAt == nil || got.Type != types.TableBucketTypeCustomer {
		t.Errorf("GetTableBucket = %+v", got)
	}

	// And: a bucket that still holds a namespace cannot be deleted
	_, err = f.tables.DeleteTableBucket(f.ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: aws.String(f.bucketARN)})
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("DeleteTableBucket (non-empty) = %s", code)
	}

	// And: once empty it can, after which it is not found
	if _, err := f.tables.DeleteNamespace(f.ctx, &s3tables.DeleteNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace),
	}); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := f.tables.DeleteTableBucket(f.ctx, &s3tables.DeleteTableBucketInput{TableBucketARN: aws.String(f.bucketARN)}); err != nil {
		t.Fatalf("DeleteTableBucket: %v", err)
	}
	_, err = f.tables.GetTableBucket(f.ctx, &s3tables.GetTableBucketInput{TableBucketARN: aws.String(f.bucketARN)})
	if code := apiErrorCode(t, err); code != "NotFoundException" {
		t.Errorf("GetTableBucket after delete = %s", code)
	}
}

func TestCreateTableBucket_rejectsDuplicatesAndBadNames(t *testing.T) {
	// Given: an existing table bucket
	f := newFixture(t, "dupe-bucket")

	cases := map[string]string{
		"dupe-bucket":           "ConflictException",
		"Upper":                 "BadRequestException",
		"has.period":            "BadRequestException",
		"has_underscore":        "BadRequestException",
		"aws-reserved":          "BadRequestException",
		"ends--table-s3":        "BadRequestException",
		"ab":                    "BadRequestException",
		strings.Repeat("a", 64): "BadRequestException",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			// When: a bucket of that name is created
			_, err := f.tables.CreateTableBucket(f.ctx, &s3tables.CreateTableBucketInput{Name: aws.String(name)})
			// Then: AWS's error comes back
			if code := apiErrorCode(t, err); code != want {
				t.Errorf("code = %s, want %s", code, want)
			}
		})
	}
}

func TestListTableBuckets_prefixAndPagination(t *testing.T) {
	// Given: five buckets, three of them sharing a prefix
	srv := helpers.NewTestServer(t)
	c := tablesClient(srv)
	ctx := context.Background()
	for _, n := range []string{"page-a", "page-b", "page-c", "other-a", "other-b"} {
		if _, err := c.CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String(n)}); err != nil {
			t.Fatalf("CreateTableBucket %s: %v", n, err)
		}
	}

	// When: the prefix is listed two at a time through the SDK paginator
	var names []string
	pages := 0
	p := s3tables.NewListTableBucketsPaginator(c, &s3tables.ListTableBucketsInput{Prefix: aws.String("page-"), MaxBuckets: aws.Int32(2)})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			t.Fatalf("ListTableBuckets: %v", err)
		}
		pages++
		for _, b := range out.TableBuckets {
			names = append(names, aws.ToString(b.Name))
		}
	}

	// Then: every matching bucket comes back once, in order, over two pages
	if strings.Join(names, ",") != "page-a,page-b,page-c" || pages != 2 {
		t.Errorf("names = %v over %d pages", names, pages)
	}

	// And: a token that is not one we issued is refused
	_, err := c.ListTableBuckets(ctx, &s3tables.ListTableBucketsInput{ContinuationToken: aws.String("garbage")})
	if code := apiErrorCode(t, err); code != "BadRequestException" {
		t.Errorf("bad token = %s", code)
	}
}

// ─── Namespaces ───────────────────────────────────────────────────────────────

func TestNamespaces(t *testing.T) {
	// Given: a bucket with one namespace
	f := newFixture(t, "ns-bucket")

	// When: another is created, a duplicate is attempted, and one is read
	if _, err := f.tables.CreateNamespace(f.ctx, &s3tables.CreateNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: []string{"sales"},
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	_, dupErr := f.tables.CreateNamespace(f.ctx, &s3tables.CreateNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: []string{"sales"},
	})
	_, badErr := f.tables.CreateNamespace(f.ctx, &s3tables.CreateNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: []string{"aws_reserved"},
	})
	got, err := f.tables.GetNamespace(f.ctx, &s3tables.GetNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String("sales"),
	})
	if err != nil {
		t.Fatalf("GetNamespace: %v", err)
	}
	list, err := f.tables.ListNamespaces(f.ctx, &s3tables.ListNamespacesInput{TableBucketARN: aws.String(f.bucketARN)})
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}

	// Then
	if code := apiErrorCode(t, dupErr); code != "ConflictException" {
		t.Errorf("duplicate namespace = %s", code)
	}
	if code := apiErrorCode(t, badErr); code != "BadRequestException" {
		t.Errorf("reserved namespace = %s", code)
	}
	if len(got.Namespace) != 1 || got.Namespace[0] != "sales" || aws.ToString(got.OwnerAccountId) != "000000000000" || got.CreatedAt == nil {
		t.Errorf("GetNamespace = %+v", got)
	}
	if len(list.Namespaces) != 2 || list.Namespaces[0].Namespace[0] != "analytics" {
		t.Errorf("ListNamespaces = %+v", list.Namespaces)
	}
}

// ─── Tables ───────────────────────────────────────────────────────────────────

func TestTable_createGetListRenameDelete(t *testing.T) {
	// Given: a bucket and namespace
	f := newFixture(t, "table-bucket")

	// When: a table is created without metadata
	created := f.createTable(t, "events", nil)

	// Then: the ARN names the table by id under its bucket
	arn := aws.ToString(created.TableARN)
	if !strings.HasPrefix(arn, f.bucketARN+"/table/") || aws.ToString(created.VersionToken) == "" {
		t.Fatalf("CreateTable = %s / %s", arn, aws.ToString(created.VersionToken))
	}

	// And: it reads back the same by ARN and by name, with no metadata location yet
	byARN, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{TableArn: aws.String(arn)})
	if err != nil {
		t.Fatalf("GetTable by ARN: %v", err)
	}
	byName, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("events"),
	})
	if err != nil {
		t.Fatalf("GetTable by name: %v", err)
	}
	if aws.ToString(byARN.TableARN) != arn || aws.ToString(byName.TableARN) != arn {
		t.Errorf("GetTable ARNs = %s / %s", aws.ToString(byARN.TableARN), aws.ToString(byName.TableARN))
	}
	if byName.MetadataLocation != nil || byName.Format != types.OpenTableFormatIceberg || byName.Type != types.TableTypeCustomer {
		t.Errorf("GetTable = %+v", byName)
	}
	warehouse := aws.ToString(byName.WarehouseLocation)
	if !strings.HasPrefix(warehouse, "s3://") || !strings.HasSuffix(warehouse, "--table-s3") {
		t.Errorf("warehouseLocation = %q", warehouse)
	}

	// And: the warehouse is a real S3 bucket clients can write to
	whBucket := strings.TrimPrefix(warehouse, "s3://")
	if _, err := s3Client(f.srv).PutObject(f.ctx, &s3.PutObjectInput{
		Bucket: aws.String(whBucket), Key: aws.String("data/part-0.parquet"), Body: bytes.NewReader([]byte("x")),
	}); err != nil {
		t.Errorf("PutObject into the warehouse: %v", err)
	}

	// When: it is renamed into another namespace
	if _, err := f.tables.CreateNamespace(f.ctx, &s3tables.CreateNamespaceInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: []string{"archive"},
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := f.tables.RenameTable(f.ctx, &s3tables.RenameTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("events"),
		NewNamespaceName: aws.String("archive"), NewName: aws.String("events_2025"),
	}); err != nil {
		t.Fatalf("RenameTable: %v", err)
	}

	// Then: the ARN still resolves, to the new name
	renamed, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{TableArn: aws.String(arn)})
	if err != nil {
		t.Fatalf("GetTable after rename: %v", err)
	}
	if aws.ToString(renamed.Name) != "events_2025" || renamed.Namespace[0] != "archive" {
		t.Errorf("renamed = %s.%s", renamed.Namespace[0], aws.ToString(renamed.Name))
	}
	list, err := f.tables.ListTables(f.ctx, &s3tables.ListTablesInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String("archive")})
	if err != nil || len(list.Tables) != 1 {
		t.Fatalf("ListTables = %+v, %v", list, err)
	}

	// And: a namespace holding a table cannot be deleted
	_, err = f.tables.DeleteNamespace(f.ctx, &s3tables.DeleteNamespaceInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String("archive")})
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("DeleteNamespace (non-empty) = %s", code)
	}

	// When: it is deleted with a stale token, then with none
	_, staleErr := f.tables.DeleteTable(f.ctx, &s3tables.DeleteTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String("archive"), Name: aws.String("events_2025"),
		VersionToken: aws.String("0000000000"),
	})
	if code := apiErrorCode(t, staleErr); code != "ConflictException" {
		t.Errorf("DeleteTable stale token = %s", code)
	}
	if _, err := f.tables.DeleteTable(f.ctx, &s3tables.DeleteTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String("archive"), Name: aws.String("events_2025"),
	}); err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}
	_, err = f.tables.GetTable(f.ctx, &s3tables.GetTableInput{TableArn: aws.String(arn)})
	if code := apiErrorCode(t, err); code != "NotFoundException" {
		t.Errorf("GetTable after delete = %s", code)
	}
}

func TestUpdateTableMetadataLocation_versionTokenConcurrency(t *testing.T) {
	// Given: a table and its current version token
	f := newFixture(t, "cas-bucket")
	created := f.createTable(t, "orders", nil)
	loc, err := f.tables.GetTableMetadataLocation(f.ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
	})
	if err != nil {
		t.Fatalf("GetTableMetadataLocation: %v", err)
	}
	token := aws.ToString(loc.VersionToken)
	if token != aws.ToString(created.VersionToken) {
		t.Errorf("token %q != create token %q", token, aws.ToString(created.VersionToken))
	}
	newLoc := aws.ToString(loc.WarehouseLocation) + "/metadata/00001-abc.metadata.json"

	// When: one writer commits with the token
	updated, err := f.tables.UpdateTableMetadataLocation(f.ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
		VersionToken: aws.String(token), MetadataLocation: aws.String(newLoc),
	})
	if err != nil {
		t.Fatalf("UpdateTableMetadataLocation: %v", err)
	}

	// Then: the pointer moves and a new token is issued
	if aws.ToString(updated.MetadataLocation) != newLoc || aws.ToString(updated.VersionToken) == token {
		t.Errorf("update = %+v", updated)
	}

	// And: a second writer holding the old token loses
	_, err = f.tables.UpdateTableMetadataLocation(f.ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
		VersionToken: aws.String(token), MetadataLocation: aws.String(newLoc),
	})
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("stale token = %s", code)
	}

	// And: a location outside the warehouse is refused
	_, err = f.tables.UpdateTableMetadataLocation(f.ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
		VersionToken: updated.VersionToken, MetadataLocation: aws.String("s3://elsewhere/metadata/x.json"),
	})
	if code := apiErrorCode(t, err); code != "BadRequestException" {
		t.Errorf("foreign location = %s", code)
	}
}

func TestCreateTable_withSchemaWritesInitialMetadata(t *testing.T) {
	// Given: a bucket and namespace
	f := newFixture(t, "schema-bucket")

	// When: a table is created with an Iceberg schema
	f.createTable(t, "clicks", &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{
		Schema: &types.IcebergSchema{Fields: []types.SchemaField{
			{Name: aws.String("id"), Type: aws.String("long"), Required: true},
			{Name: aws.String("url"), Type: aws.String("string")},
		}},
		Properties: map[string]string{"write.format.default": "parquet"},
	}})

	// Then: the table points at a metadata.json in its warehouse
	got, err := f.tables.GetTable(f.ctx, &s3tables.GetTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("clicks"),
	})
	if err != nil {
		t.Fatalf("GetTable: %v", err)
	}
	loc := aws.ToString(got.MetadataLocation)
	warehouse := aws.ToString(got.WarehouseLocation)
	if !strings.HasPrefix(loc, warehouse+"/metadata/00000-") || !strings.HasSuffix(loc, ".metadata.json") {
		t.Fatalf("metadataLocation = %q (warehouse %q)", loc, warehouse)
	}

	// And: that file is a v2 Iceberg document describing the schema
	bucket, key, _ := strings.Cut(strings.TrimPrefix(loc, "s3://"), "/")
	obj, err := s3Client(f.srv).GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("GetObject metadata: %v", err)
	}
	defer obj.Body.Close()
	raw, _ := io.ReadAll(obj.Body)
	var doc struct {
		FormatVersion int    `json:"format-version"`
		Location      string `json:"location"`
		Schemas       []struct {
			Fields []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"schemas"`
		Properties map[string]string `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("metadata is not JSON: %v", err)
	}
	if doc.FormatVersion != 2 || doc.Location != warehouse || len(doc.Schemas) != 1 ||
		len(doc.Schemas[0].Fields) != 2 || doc.Schemas[0].Fields[1].ID != 2 || doc.Properties["write.format.default"] != "parquet" {
		t.Errorf("metadata = %s", raw)
	}
}

func TestCreateTable_badSchemaLeavesNothingBehind(t *testing.T) {
	// Given: a bucket and namespace
	f := newFixture(t, "badschema-bucket")

	// When: a table's schema has a type Iceberg does not know
	_, err := f.tables.CreateTable(f.ctx, &s3tables.CreateTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("broken"),
		Format: types.OpenTableFormatIceberg,
		Metadata: &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{Schema: &types.IcebergSchema{
			Fields: []types.SchemaField{{Name: aws.String("x"), Type: aws.String("varchar")}},
		}}},
	})

	// Then: it is refused and no table exists
	if code := apiErrorCode(t, err); code != "BadRequestException" {
		t.Errorf("code = %s", code)
	}
	list, err := f.tables.ListTables(f.ctx, &s3tables.ListTablesInput{TableBucketARN: aws.String(f.bucketARN)})
	if err != nil || len(list.Tables) != 0 {
		t.Errorf("ListTables = %+v, %v", list, err)
	}
}

// ─── Configuration ────────────────────────────────────────────────────────────

func TestPoliciesEncryptionStorageClassAndMetrics(t *testing.T) {
	// Given: a bucket configured for KMS and intelligent tiering
	f := newFixture(t, "cfg-bucket")
	kms := "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	if _, err := f.tables.PutTableBucketEncryption(f.ctx, &s3tables.PutTableBucketEncryptionInput{
		TableBucketARN:          aws.String(f.bucketARN),
		EncryptionConfiguration: &types.EncryptionConfiguration{SseAlgorithm: types.SSEAlgorithmAwsKms, KmsKeyArn: aws.String(kms)},
	}); err != nil {
		t.Fatalf("PutTableBucketEncryption: %v", err)
	}
	if _, err := f.tables.PutTableBucketStorageClass(f.ctx, &s3tables.PutTableBucketStorageClassInput{
		TableBucketARN:            aws.String(f.bucketARN),
		StorageClassConfiguration: &types.StorageClassConfiguration{StorageClass: types.StorageClassIntelligentTiering},
	}); err != nil {
		t.Fatalf("PutTableBucketStorageClass: %v", err)
	}

	// When: a table is created, and policies and metrics are configured
	f.createTable(t, "t1", nil)
	policy := `{"Version":"2012-10-17","Statement":[]}`
	if _, err := f.tables.PutTableBucketPolicy(f.ctx, &s3tables.PutTableBucketPolicyInput{TableBucketARN: aws.String(f.bucketARN), ResourcePolicy: aws.String(policy)}); err != nil {
		t.Fatalf("PutTableBucketPolicy: %v", err)
	}
	if _, err := f.tables.PutTablePolicy(f.ctx, &s3tables.PutTablePolicyInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1"), ResourcePolicy: aws.String(policy),
	}); err != nil {
		t.Fatalf("PutTablePolicy: %v", err)
	}
	_, noMetricsErr := f.tables.GetTableBucketMetricsConfiguration(f.ctx, &s3tables.GetTableBucketMetricsConfigurationInput{TableBucketARN: aws.String(f.bucketARN)})
	if _, err := f.tables.PutTableBucketMetricsConfiguration(f.ctx, &s3tables.PutTableBucketMetricsConfigurationInput{TableBucketARN: aws.String(f.bucketARN)}); err != nil {
		t.Fatalf("PutTableBucketMetricsConfiguration: %v", err)
	}

	// Then: the table inherited the bucket's encryption and storage class
	enc, err := f.tables.GetTableEncryption(f.ctx, &s3tables.GetTableEncryptionInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1")})
	if err != nil || enc.EncryptionConfiguration.SseAlgorithm != types.SSEAlgorithmAwsKms || aws.ToString(enc.EncryptionConfiguration.KmsKeyArn) != kms {
		t.Errorf("GetTableEncryption = %+v, %v", enc, err)
	}
	sc, err := f.tables.GetTableStorageClass(f.ctx, &s3tables.GetTableStorageClassInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1")})
	if err != nil || sc.StorageClassConfiguration.StorageClass != types.StorageClassIntelligentTiering {
		t.Errorf("GetTableStorageClass = %+v, %v", sc, err)
	}

	// And: policies read back, and delete removes them
	bp, err := f.tables.GetTableBucketPolicy(f.ctx, &s3tables.GetTableBucketPolicyInput{TableBucketARN: aws.String(f.bucketARN)})
	if err != nil || aws.ToString(bp.ResourcePolicy) != policy {
		t.Errorf("GetTableBucketPolicy = %+v, %v", bp, err)
	}
	if _, err := f.tables.DeleteTablePolicy(f.ctx, &s3tables.DeleteTablePolicyInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1")}); err != nil {
		t.Fatalf("DeleteTablePolicy: %v", err)
	}
	_, err = f.tables.GetTablePolicy(f.ctx, &s3tables.GetTablePolicyInput{TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1")})
	if code := apiErrorCode(t, err); code != "NotFoundException" {
		t.Errorf("GetTablePolicy after delete = %s", code)
	}

	// And: metrics were absent until configured
	if code := apiErrorCode(t, noMetricsErr); code != "NotFoundException" {
		t.Errorf("metrics before put = %s", code)
	}
	m, err := f.tables.GetTableBucketMetricsConfiguration(f.ctx, &s3tables.GetTableBucketMetricsConfigurationInput{TableBucketARN: aws.String(f.bucketARN)})
	if err != nil || aws.ToString(m.TableBucketARN) != f.bucketARN || aws.ToString(m.Id) == "" {
		t.Errorf("GetTableBucketMetricsConfiguration = %+v, %v", m, err)
	}
}

func TestMaintenanceIsStoredAndNothingRuns(t *testing.T) {
	// Given: a table
	f := newFixture(t, "maint-bucket")
	f.createTable(t, "t1", nil)

	// When: compaction is turned off
	if _, err := f.tables.PutTableMaintenanceConfiguration(f.ctx, &s3tables.PutTableMaintenanceConfigurationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1"),
		Type:  types.TableMaintenanceTypeIcebergCompaction,
		Value: &types.TableMaintenanceConfigurationValue{Status: types.MaintenanceStatusDisabled},
	}); err != nil {
		t.Fatalf("PutTableMaintenanceConfiguration: %v", err)
	}

	// Then: the configuration echoes it beside the default snapshot management
	cfg, err := f.tables.GetTableMaintenanceConfiguration(f.ctx, &s3tables.GetTableMaintenanceConfigurationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1"),
	})
	if err != nil {
		t.Fatalf("GetTableMaintenanceConfiguration: %v", err)
	}
	if cfg.Configuration["icebergCompaction"].Status != types.MaintenanceStatusDisabled ||
		cfg.Configuration["icebergSnapshotManagement"].Status != types.MaintenanceStatusEnabled {
		t.Errorf("configuration = %+v", cfg.Configuration)
	}

	// And: job status says nothing has run
	st, err := f.tables.GetTableMaintenanceJobStatus(f.ctx, &s3tables.GetTableMaintenanceJobStatusInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1"),
	})
	if err != nil {
		t.Fatalf("GetTableMaintenanceJobStatus: %v", err)
	}
	if st.Status["icebergCompaction"].Status != types.JobStatusDisabled ||
		st.Status["icebergSnapshotManagement"].Status != types.JobStatusNotYetRun ||
		st.Status["icebergUnreferencedFileRemoval"].Status != types.JobStatusNotYetRun {
		t.Errorf("job status = %+v", st.Status)
	}

	// And: the wrong settings for a type are refused
	_, err = f.tables.PutTableMaintenanceConfiguration(f.ctx, &s3tables.PutTableMaintenanceConfigurationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("t1"),
		Type: types.TableMaintenanceTypeIcebergCompaction,
		Value: &types.TableMaintenanceConfigurationValue{Settings: &types.TableMaintenanceSettingsMemberIcebergSnapshotManagement{
			Value: types.IcebergSnapshotManagementSettings{MinSnapshotsToKeep: aws.Int32(2)},
		}},
	})
	if code := apiErrorCode(t, err); code != "BadRequestException" {
		t.Errorf("mismatched settings = %s", code)
	}
}

func TestReplicationAndRecordExpiration(t *testing.T) {
	// Given: a table
	f := newFixture(t, "repl-bucket")
	arn := aws.ToString(f.createTable(t, "t1", nil).TableARN)
	cfg := &types.TableReplicationConfiguration{
		Role: aws.String("arn:aws:iam::000000000000:role/replication"),
		Rules: []types.TableReplicationRule{{Destinations: []types.ReplicationDestination{
			{DestinationTableBucketARN: aws.String("arn:aws:s3tables:us-west-2:000000000000:bucket/replica")},
		}}},
	}

	// When: replication is configured
	put, err := f.tables.PutTableReplication(f.ctx, &s3tables.PutTableReplicationInput{TableArn: aws.String(arn), Configuration: cfg})
	if err != nil {
		t.Fatalf("PutTableReplication: %v", err)
	}

	// Then: it reads back, and status reports every destination pending
	got, err := f.tables.GetTableReplication(f.ctx, &s3tables.GetTableReplicationInput{TableArn: aws.String(arn)})
	if err != nil || aws.ToString(got.VersionToken) != aws.ToString(put.VersionToken) {
		t.Fatalf("GetTableReplication = %+v, %v", got, err)
	}
	status, err := f.tables.GetTableReplicationStatus(f.ctx, &s3tables.GetTableReplicationStatusInput{TableArn: aws.String(arn)})
	if err != nil || len(status.Destinations) != 1 || status.Destinations[0].ReplicationStatus != types.ReplicationStatusPending {
		t.Errorf("GetTableReplicationStatus = %+v, %v", status, err)
	}

	// And: a stale token cannot delete it, the current one can
	_, err = f.tables.DeleteTableReplication(f.ctx, &s3tables.DeleteTableReplicationInput{TableArn: aws.String(arn), VersionToken: aws.String("stale")})
	if code := apiErrorCode(t, err); code != "ConflictException" {
		t.Errorf("stale delete = %s", code)
	}
	if _, err := f.tables.DeleteTableReplication(f.ctx, &s3tables.DeleteTableReplicationInput{TableArn: aws.String(arn), VersionToken: put.VersionToken}); err != nil {
		t.Fatalf("DeleteTableReplication: %v", err)
	}

	// When: record expiration is turned on
	if _, err := f.tables.PutTableRecordExpirationConfiguration(f.ctx, &s3tables.PutTableRecordExpirationConfigurationInput{
		TableArn: aws.String(arn),
		Value:    &types.TableRecordExpirationConfigurationValue{Status: types.TableRecordExpirationStatusEnabled, Settings: &types.TableRecordExpirationSettings{Days: aws.Int32(30)}},
	}); err != nil {
		t.Fatalf("PutTableRecordExpirationConfiguration: %v", err)
	}

	// Then: it echoes, and no job has run
	exp, err := f.tables.GetTableRecordExpirationConfiguration(f.ctx, &s3tables.GetTableRecordExpirationConfigurationInput{TableArn: aws.String(arn)})
	if err != nil || aws.ToInt32(exp.Configuration.Settings.Days) != 30 {
		t.Errorf("GetTableRecordExpirationConfiguration = %+v, %v", exp, err)
	}
	job, err := f.tables.GetTableRecordExpirationJobStatus(f.ctx, &s3tables.GetTableRecordExpirationJobStatusInput{TableArn: aws.String(arn)})
	if err != nil || job.Status != types.TableRecordExpirationJobStatusNotYetRun {
		t.Errorf("GetTableRecordExpirationJobStatus = %+v, %v", job, err)
	}
}

func TestTags_onBucketsAndTables(t *testing.T) {
	// Given: a bucket created with tags, and a table
	srv := helpers.NewTestServer(t)
	c := tablesClient(srv)
	ctx := context.Background()
	b, err := c.CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String("tag-bucket"), Tags: map[string]string{"team": "data"}})
	if err != nil {
		t.Fatalf("CreateTableBucket: %v", err)
	}
	if _, err := c.CreateNamespace(ctx, &s3tables.CreateNamespaceInput{TableBucketARN: b.Arn, Namespace: []string{"ns"}}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	tbl, err := c.CreateTable(ctx, &s3tables.CreateTableInput{TableBucketARN: b.Arn, Namespace: aws.String("ns"), Name: aws.String("t"), Format: types.OpenTableFormatIceberg})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	// When: the table is tagged and the bucket untagged
	if _, err := c.TagResource(ctx, &s3tables.TagResourceInput{ResourceArn: tbl.TableARN, Tags: map[string]string{"env": "dev", "tier": "gold"}}); err != nil {
		t.Fatalf("TagResource: %v", err)
	}
	if _, err := c.UntagResource(ctx, &s3tables.UntagResourceInput{ResourceArn: b.Arn, TagKeys: []string{"team"}}); err != nil {
		t.Fatalf("UntagResource: %v", err)
	}

	// Then: each resource lists its own tags
	bt, err := c.ListTagsForResource(ctx, &s3tables.ListTagsForResourceInput{ResourceArn: b.Arn})
	if err != nil || len(bt.Tags) != 0 {
		t.Errorf("bucket tags = %v, %v", bt, err)
	}
	tt, err := c.ListTagsForResource(ctx, &s3tables.ListTagsForResourceInput{ResourceArn: tbl.TableARN})
	if err != nil || tt.Tags["env"] != "dev" || tt.Tags["tier"] != "gold" {
		t.Errorf("table tags = %v, %v", tt, err)
	}
}

// ─── Routing ──────────────────────────────────────────────────────────────────

// Every S3 Tables root is also a legal S3 bucket name. The router dispatches
// on the SigV4 signing name, so an S3 bucket named after one keeps working.
func TestS3BucketsNamedLikeS3TablesRootsStillWork(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c := s3Client(srv)
	tables := tablesClient(srv)
	ctx := context.Background()

	for _, root := range s3tablessvc.Roots {
		bucket := strings.TrimPrefix(root, "/")
		t.Run(bucket, func(t *testing.T) {
			// Given: an S3 bucket named like an S3 Tables root, holding an object
			if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Fatalf("CreateBucket: %v", err)
			}
			if _, err := c.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(bucket), Key: aws.String("arn%3Aaws/key.txt"), Body: bytes.NewReader([]byte("payload")),
			}); err != nil {
				t.Fatalf("PutObject: %v", err)
			}

			// When: S3 reads and lists it
			obj, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("arn%3Aaws/key.txt")})
			if err != nil {
				t.Fatalf("GetObject: %v", err)
			}
			body, _ := io.ReadAll(obj.Body)
			obj.Body.Close()
			list, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
			if err != nil {
				t.Fatalf("ListObjectsV2: %v", err)
			}

			// Then: S3 answers, not S3 Tables
			if string(body) != "payload" || len(list.Contents) != 1 {
				t.Errorf("body %q, %d keys", body, len(list.Contents))
			}
			if _, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Errorf("HeadBucket: %v", err)
			}
			if _, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("arn%3Aaws/key.txt")}); err != nil {
				t.Errorf("DeleteObject: %v", err)
			}
			if _, err := c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
				t.Errorf("DeleteBucket: %v", err)
			}
		})
	}

	// And: an unsigned path-style read reaches S3 too
	if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("tables")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("tables"), Key: aws.String("arn%3Aaws/key.txt"), Body: bytes.NewReader([]byte("payload")),
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	resp, err := http.Get(srv.URL + "/tables/arn%253Aaws/key.txt")
	if err != nil {
		t.Fatalf("unsigned GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "payload" {
		t.Errorf("unsigned GET /tables/... = %d %q", resp.StatusCode, body)
	}

	// And: signed s3tables calls on the same roots still reach S3 Tables
	if _, err := tables.CreateTableBucket(ctx, &s3tables.CreateTableBucketInput{Name: aws.String("routed")}); err != nil {
		t.Fatalf("CreateTableBucket: %v", err)
	}
	out, err := tables.ListTableBuckets(ctx, &s3tables.ListTableBucketsInput{})
	if err != nil || len(out.TableBuckets) != 1 || aws.ToString(out.TableBuckets[0].Name) != "routed" {
		t.Errorf("ListTableBuckets = %+v, %v", out, err)
	}
}

func TestCreateBucket_refusesTheWarehouseSuffix(t *testing.T) {
	// Given: a server
	srv := helpers.NewTestServer(t)

	// When: an ordinary CreateBucket asks for a "--table-s3" name
	_, err := s3Client(srv).CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("mine--table-s3")})

	// Then: S3 refuses the suffix it reserves for S3 Tables
	if code := apiErrorCode(t, err); code != "InvalidBucketName" {
		t.Errorf("code = %s", code)
	}
}

// schemaV2 (nested types as documents) is not emulated: it must be a 501, not
// a table whose metadata silently lacks the columns asked for.
func TestCreateTable_schemaV2IsNotImplemented(t *testing.T) {
	f := newFixture(t, "v2-bucket")
	_, err := f.tables.CreateTable(f.ctx, &s3tables.CreateTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("v2"),
		Format: types.OpenTableFormatIceberg,
		Metadata: &types.TableMetadataMemberIceberg{Value: types.IcebergMetadata{SchemaV2: &types.IcebergSchemaV2{
			Type: types.SchemaV2FieldTypeStruct,
			Fields: []types.SchemaV2Field{{Id: aws.Int32(1), Name: aws.String("id"), Required: aws.Bool(true),
				Type: document.NewLazyDocument("long")}},
		}}},
	})
	var re interface{ HTTPStatusCode() int }
	if !errors.As(err, &re) || re.HTTPStatusCode() != http.StatusNotImplemented {
		t.Errorf("err = %v, want a 501", err)
	}
}
