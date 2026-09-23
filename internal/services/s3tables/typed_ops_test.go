package s3tables

import (
	"context"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/state"
)

func newTestService(t *testing.T) (*Service, state.Store) {
	t.Helper()
	st := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1", AccountID: "111122223333"}
	return New(cfg, st, zap.NewNop(), clock.NewMock()), st
}

// seed creates a bucket and a namespace and returns the bucket ARN.
func seed(t *testing.T, s *Service) string {
	t.Helper()
	ctx := context.Background()
	out, aerr := s.createTableBucketTyped(ctx, &createTableBucketRequest{Name: "seed"})
	if aerr != nil {
		t.Fatalf("createTableBucket: %v", aerr)
	}
	if _, aerr := s.createNamespaceTyped(ctx, &createNamespaceRequest{TableBucketARN: out.ARN, Namespace: []string{"ns"}}); aerr != nil {
		t.Fatalf("createNamespace: %v", aerr)
	}
	return out.ARN
}

func TestTypedOps_coverEveryModeledOperation(t *testing.T) {
	// Given: the service's operation registry
	ops := (&Service{}).typedOps()

	// Then: all 49 modeled operations are registered as typed, not raw, ops
	if len(ops) != 49 {
		t.Fatalf("typed ops = %d, want 49", len(ops))
	}
	for name, o := range ops {
		if o.Name() != name {
			t.Errorf("op %q has Name() %q", name, o.Name())
		}
		if _, raw := o.(*op.Raw); raw {
			t.Errorf("%s registered as raw", name)
		}
	}
}

func TestRootRouters_mountEveryRoot(t *testing.T) {
	s, _ := newTestService(t)
	routers := s.RootRouters()
	for _, root := range Roots {
		if routers[root] == nil {
			t.Errorf("no router for %s", root)
		}
	}
}

func TestValidateNames(t *testing.T) {
	buckets := map[string]bool{
		"my-bucket": true, "abc": true, strings.Repeat("a", 63): true,
		"ab": false, "My-Bucket": false, "my.bucket": false, "my_bucket": false, "-bucket": false,
		"bucket-": false, "aws-bucket": false, "xn--bucket": false, "sthree-b": false,
		"amzn-s3-demo-b": false, "b-s3alias": false, "b--ol-s3": false, "b--x-s3": false, "b--table-s3": false,
	}
	for name, ok := range buckets {
		if got := validateTableBucketName(name) == nil; got != ok {
			t.Errorf("bucket %q valid = %v, want %v", name, got, ok)
		}
	}
	namespaces := map[string]bool{
		"sales": true, "sales_2025": true, "1st": true, strings.Repeat("a", 255): true,
		"": false, "_leading": false, "has-hyphen": false, "has.period": false, "Upper": false,
		"aws": false, "aws_data": false, strings.Repeat("a", 256): false,
	}
	for name, ok := range namespaces {
		if got := validateNamespaceName(name) == nil; got != ok {
			t.Errorf("namespace %q valid = %v, want %v", name, got, ok)
		}
	}
	// Tables share the rules, without the reserved prefix.
	if validateTableName("aws_table") != nil || validateTableName("bad-name") == nil {
		t.Error("table name rules")
	}
}

func TestParseARNs(t *testing.T) {
	b, aerr := parseBucketARN("arn:aws:s3tables:eu-west-1:111122223333:bucket/my-bucket")
	if aerr != nil || b.Region != "eu-west-1" || b.Account != "111122223333" || b.Bucket != "my-bucket" || b.TableID != "" {
		t.Errorf("bucket ARN = %+v, %v", b, aerr)
	}
	tbl, aerr := parseTableARN("arn:aws:s3tables:us-east-1:111122223333:bucket/my-bucket/table/0f3a-uuid")
	if aerr != nil || tbl.Bucket != "my-bucket" || tbl.TableID != "0f3a-uuid" {
		t.Errorf("table ARN = %+v, %v", tbl, aerr)
	}
	for _, bad := range []string{
		"", "arn:aws:s3:::my-bucket", "arn:aws:s3tables:us-east-1:1234:bucket/b",
		"arn:aws:s3tables:us-east-1:111122223333:bucket/bkt/table/x", // table ARN is not a bucket ARN
	} {
		if _, aerr := parseBucketARN(bad); aerr == nil {
			t.Errorf("parseBucketARN(%q) accepted", bad)
		}
	}
	if r, aerr := parseResourceARN("arn:aws:s3tables:us-east-1:111122223333:bucket/bkt/table/x"); aerr != nil || r.TableID != "x" {
		t.Errorf("resource ARN = %+v, %v", r, aerr)
	}
}

func TestResolveBucket_otherRegionOrAccountIsNotFound(t *testing.T) {
	s, _ := newTestService(t)
	arn := seed(t, s)
	ctx := context.Background()
	for _, other := range []string{
		strings.Replace(arn, "us-east-1", "eu-west-1", 1),
		strings.Replace(arn, "111122223333", "999999999999", 1),
	} {
		if _, aerr := s.resolveBucket(ctx, other); aerr == nil || aerr.Code != "NotFoundException" {
			t.Errorf("resolveBucket(%q) = %v", other, aerr)
		}
	}
}

func TestUpdateTableMetadataLocation_exactlyOneConcurrentWriterWins(t *testing.T) {
	// Given: a table and its version token
	s, _ := newTestService(t)
	arn := seed(t, s)
	ctx := context.Background()
	created, aerr := s.createTableTyped(ctx, &createTableRequest{TableBucketARN: arn, Namespace: "ns", Name: "t", Format: formatIceberg})
	if aerr != nil {
		t.Fatalf("createTable: %v", aerr)
	}
	loc, _ := s.getTableMetadataLocationTyped(ctx, &tableRequest{TableBucketARN: arn, Namespace: "ns", Name: "t"})

	// When: many writers commit from the same token at once
	const writers = 16
	var wg sync.WaitGroup
	results := make(chan *protocol.AWSError, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, aerr := s.updateTableMetadataLocationTyped(ctx, &updateMetadataLocationRequest{
				TableBucketARN: arn, Namespace: "ns", Name: "t",
				VersionToken: created.VersionToken, MetadataLocation: loc.WarehouseLocation + "/metadata/1.json",
			})
			results <- aerr
		}()
	}
	wg.Wait()
	close(results)

	// Then: exactly one succeeds and the rest get ConflictException
	wins := 0
	for aerr := range results {
		switch {
		case aerr == nil:
			wins++
		case aerr.Code != "ConflictException":
			t.Errorf("loser got %s", aerr.Code)
		}
	}
	if wins != 1 {
		t.Errorf("%d writers won", wins)
	}
}

func TestListings_skipMalformedRecords(t *testing.T) {
	// Given: a bucket and namespace, plus a corrupt record beside each
	s, st := newTestService(t)
	arn := seed(t, s)
	ctx := context.Background()
	if _, aerr := s.createTableTyped(ctx, &createTableRequest{TableBucketARN: arn, Namespace: "ns", Name: "good", Format: formatIceberg}); aerr != nil {
		t.Fatalf("createTable: %v", aerr)
	}
	_ = st.Set(ctx, nsBuckets, "us-east-1/corrupt", "{not json")
	_ = st.Set(ctx, nsNamespaces, "us-east-1/seed/corrupt", "{not json")
	_ = st.Set(ctx, nsTables, "us-east-1/seed/ns/corrupt", "{not json")

	// When: everything is listed
	buckets, aerr := s.listTableBucketsTyped(ctx, &listTableBucketsRequest{})
	if aerr != nil {
		t.Fatalf("listTableBuckets: %v", aerr)
	}
	nss, aerr := s.listNamespacesTyped(ctx, &listNamespacesRequest{TableBucketARN: arn})
	if aerr != nil {
		t.Fatalf("listNamespaces: %v", aerr)
	}
	tables, aerr := s.listTablesTyped(ctx, &listTablesRequest{TableBucketARN: arn})
	if aerr != nil {
		t.Fatalf("listTables: %v", aerr)
	}

	// Then: the good records are listed and the corrupt ones skipped
	if len(buckets.TableBuckets) != 1 || len(nss.Namespaces) != 1 || len(tables.Tables) != 1 {
		t.Errorf("listed %d buckets, %d namespaces, %d tables", len(buckets.TableBuckets), len(nss.Namespaces), len(tables.Tables))
	}

	// And: a corrupt named record reads as not found, not a 500
	corruptARN := s.bucketARN("us-east-1", "corrupt")
	if _, aerr := s.getTableBucketTyped(ctx, &tableBucketARNRequest{TableBucketARN: corruptARN}); aerr == nil || aerr.Code != "NotFoundException" {
		t.Errorf("corrupt bucket = %v", aerr)
	}
	if _, _, aerr := s.resolveTable(ctx, arn, "ns", "corrupt"); aerr == nil || aerr.Code != "NotFoundException" {
		t.Errorf("corrupt table = %v", aerr)
	}
}

func TestListTables_paginatesAndRejectsBadLimits(t *testing.T) {
	s, _ := newTestService(t)
	arn := seed(t, s)
	ctx := context.Background()
	for _, n := range []string{"a", "b", "c"} {
		if _, aerr := s.createTableTyped(ctx, &createTableRequest{TableBucketARN: arn, Namespace: "ns", Name: n, Format: formatIceberg}); aerr != nil {
			t.Fatalf("createTable: %v", aerr)
		}
	}
	two := 2
	first, aerr := s.listTablesTyped(ctx, &listTablesRequest{TableBucketARN: arn, MaxTables: &two})
	if aerr != nil || len(first.Tables) != 2 || first.ContinuationToken == "" {
		t.Fatalf("first page = %+v, %v", first, aerr)
	}
	second, aerr := s.listTablesTyped(ctx, &listTablesRequest{TableBucketARN: arn, MaxTables: &two, ContinuationToken: first.ContinuationToken})
	if aerr != nil || len(second.Tables) != 1 || second.ContinuationToken != "" || second.Tables[0].Name != "c" {
		t.Errorf("second page = %+v, %v", second, aerr)
	}
	for _, bad := range []int{0, 1001} {
		if _, aerr := s.listTablesTyped(ctx, &listTablesRequest{TableBucketARN: arn, MaxTables: &bad}); aerr == nil || aerr.Code != "BadRequestException" {
			t.Errorf("maxTables=%d = %v", bad, aerr)
		}
	}
}

func TestCreateTable_warehouseAndMetadataGoThroughTheS3Accessor(t *testing.T) {
	// Given: a service wired to a recording S3 accessor
	s, _ := newTestService(t)
	var ensured []string
	var written []string
	s.InitS3Access(
		func(_ context.Context, bucket, region string) *protocol.AWSError {
			ensured = append(ensured, region+":"+bucket)
			return nil
		},
		func(_ context.Context, bucket, key string, _ []byte, _ events.S3PutObjectOptions) (events.S3PutObjectResult, *protocol.AWSError) {
			written = append(written, bucket+"/"+key)
			return events.S3PutObjectResult{}, nil
		},
	)
	arn := seed(t, s)
	ctx := context.Background()

	// When: a table is created with a schema
	_, aerr := s.createTableTyped(ctx, &createTableRequest{
		TableBucketARN: arn, Namespace: "ns", Name: "t", Format: formatIceberg,
		Metadata: &tableMetadata{Iceberg: &icebergMetadata{Schema: &icebergSchema{Fields: []icebergSchemaField{{Name: "id", Type: "int"}}}}},
	})
	if aerr != nil {
		t.Fatalf("createTable: %v", aerr)
	}

	// Then: one 63-character warehouse bucket was ensured and one metadata file written into it
	if len(ensured) != 1 || len(written) != 1 {
		t.Fatalf("ensured %v, written %v", ensured, written)
	}
	bucket := strings.TrimPrefix(ensured[0], "us-east-1:")
	if len(bucket) != 63 || !strings.HasSuffix(bucket, "--table-s3") {
		t.Errorf("warehouse bucket %q", bucket)
	}
	if !strings.HasPrefix(written[0], bucket+"/metadata/00000-") {
		t.Errorf("metadata written to %q", written[0])
	}
}
