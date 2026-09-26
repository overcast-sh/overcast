package glue

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// fakeS3Tables is an in-memory events.S3TablesCatalog: bucket "lake" with
// namespace "sales", which holds tables "orders" and "refunds".
type fakeS3Tables struct {
	buckets    []events.S3TableBucket
	namespaces map[string][]events.S3TablesNamespace
	tables     map[string][]events.S3TablesTable
}

var fakeCreated = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func newFakeS3Tables() *fakeS3Tables {
	return &fakeS3Tables{
		buckets: []events.S3TableBucket{{Name: "lake", ARN: "arn:aws:s3tables:us-east-1:123456789012:bucket/lake", CreatedAt: fakeCreated}},
		namespaces: map[string][]events.S3TablesNamespace{
			"lake": {{Name: "sales", CreatedAt: fakeCreated}},
		},
		tables: map[string][]events.S3TablesTable{
			"lake/sales": {
				{Name: "orders", Namespace: "sales", MetadataLocation: "s3://w--table-s3/metadata/00000-a.metadata.json",
					WarehouseLocation: "s3://w--table-s3", CreatedAt: fakeCreated, ModifiedAt: fakeCreated.Add(time.Hour),
					Columns: []events.S3TablesColumn{{Name: "id", Type: "bigint"}, {Name: "amount", Type: "decimal(10,2)"}}},
				{Name: "refunds", Namespace: "sales", WarehouseLocation: "s3://r--table-s3", CreatedAt: fakeCreated},
			},
		},
	}
}

func (f *fakeS3Tables) ListTableBuckets(context.Context) ([]events.S3TableBucket, error) {
	return f.buckets, nil
}

func (f *fakeS3Tables) GetTableBucket(_ context.Context, name string) (events.S3TableBucket, bool, error) {
	for _, b := range f.buckets {
		if b.Name == name {
			return b, true, nil
		}
	}
	return events.S3TableBucket{}, false, nil
}

func (f *fakeS3Tables) ListNamespaces(_ context.Context, bucket string) ([]events.S3TablesNamespace, error) {
	return f.namespaces[bucket], nil
}

func (f *fakeS3Tables) GetNamespace(_ context.Context, bucket, name string) (events.S3TablesNamespace, bool, error) {
	for _, n := range f.namespaces[bucket] {
		if n.Name == name {
			return n, true, nil
		}
	}
	return events.S3TablesNamespace{}, false, nil
}

func (f *fakeS3Tables) ListTables(_ context.Context, bucket, namespace string) ([]events.S3TablesTable, error) {
	return f.tables[bucket+"/"+namespace], nil
}

func (f *fakeS3Tables) GetTable(_ context.Context, bucket, namespace, name string) (events.S3TablesTable, bool, error) {
	for _, t := range f.tables[bucket+"/"+namespace] {
		if t.Name == name {
			return t, true, nil
		}
	}
	return events.S3TablesTable{}, false, nil
}

func newFederatedService(t *testing.T) *Service {
	t.Helper()
	s, _, _ := newTestService(t)
	s.InitS3Tables(newFakeS3Tables())
	return s
}

const lakeCatalogID = "123456789012:s3tablescatalog/lake"

func TestGetCatalog_answersTheRootS3TablesCatalogAndABucketsCatalog(t *testing.T) {
	s := newFederatedService(t)
	ctx := context.Background()

	// When / Then: the account ID is the root catalog
	root, aerr := s.getCatalogTyped(ctx, &getCatalogReq{CatalogId: "123456789012"})
	mustOK(t, "GetCatalog(root)", aerr)
	if root.Catalog.CatalogId != "123456789012" || root.Catalog.ResourceArn != "arn:aws:glue:us-east-1:123456789012:catalog" {
		t.Errorf("root = %+v", root.Catalog)
	}

	// And: s3tablescatalog, with or without the account, is the federated parent
	for _, id := range []string{"s3tablescatalog", "123456789012:s3tablescatalog"} {
		out, aerr := s.getCatalogTyped(ctx, &getCatalogReq{CatalogId: id})
		mustOK(t, "GetCatalog("+id+")", aerr)
		c := out.Catalog
		if c.Name != "s3tablescatalog" || c.CatalogId != "123456789012:s3tablescatalog" ||
			c.ResourceArn != "arn:aws:glue:us-east-1:123456789012:catalog/s3tablescatalog" ||
			c.FederatedCatalog == nil || c.FederatedCatalog.Identifier != "arn:aws:s3tables:us-east-1:123456789012:bucket/*" ||
			c.FederatedCatalog.ConnectionName != "aws:s3tables" {
			t.Errorf("GetCatalog(%s) = %+v", id, c)
		}
	}

	// And: a bucket's catalog is named by the bucket and federated to it
	out, aerr := s.getCatalogTyped(ctx, &getCatalogReq{CatalogId: lakeCatalogID})
	mustOK(t, "GetCatalog(lake)", aerr)
	c := out.Catalog
	if c.Name != "lake" || c.CatalogId != lakeCatalogID ||
		c.ResourceArn != "arn:aws:glue:us-east-1:123456789012:catalog/s3tablescatalog/lake" ||
		c.FederatedCatalog.Identifier != "arn:aws:s3tables:us-east-1:123456789012:bucket/lake" ||
		c.CreateTime != epochSeconds(fakeCreated) {
		t.Errorf("GetCatalog(lake) = %+v", c)
	}

	// And: an ID naming nothing is EntityNotFoundException
	for _, id := range []string{"123456789012:s3tablescatalog/absent", "999999999999:s3tablescatalog", "other"} {
		_, aerr := s.getCatalogTyped(ctx, &getCatalogReq{CatalogId: id})
		wantCode(t, "GetCatalog("+id+")", aerr, codeEntityNotFound)
	}
	_, aerr = s.getCatalogTyped(ctx, &getCatalogReq{})
	wantCode(t, "GetCatalog()", aerr, codeInvalidInput)
}

func catalogNames(list []catalogInfo) string {
	names := make([]string, len(list))
	for i, c := range list {
		names[i] = c.Name
	}
	return strings.Join(names, ",")
}

func TestGetCatalogs_listsTheHierarchy(t *testing.T) {
	s := newFederatedService(t)
	ctx := context.Background()
	tests := []struct {
		name string
		req  getCatalogsReq
		want string
	}{
		{"account level", getCatalogsReq{}, "s3tablescatalog"},
		{"with the root", getCatalogsReq{ParentCatalogId: "123456789012", IncludeRoot: ptr(true)}, "123456789012,s3tablescatalog"},
		{"recursive", getCatalogsReq{Recursive: true}, "s3tablescatalog,lake"},
		{"under s3tablescatalog", getCatalogsReq{ParentCatalogId: "s3tablescatalog"}, "lake"},
		{"under a bucket", getCatalogsReq{ParentCatalogId: lakeCatalogID}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			out, aerr := s.getCatalogsTyped(ctx, &tt.req)

			// Then
			mustOK(t, "GetCatalogs", aerr)
			if got := catalogNames(out.CatalogList); got != tt.want {
				t.Errorf("catalogs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetCatalogs_refusesWhatAWSRefuses(t *testing.T) {
	s := newFederatedService(t)
	ctx := context.Background()

	// IncludeRoot below the account level is InvalidInputException
	_, aerr := s.getCatalogsTyped(ctx, &getCatalogsReq{ParentCatalogId: "s3tablescatalog", IncludeRoot: ptr(false)})
	wantCode(t, "GetCatalogs(IncludeRoot under s3tablescatalog)", aerr, codeInvalidInput)

	// A parent that does not exist is EntityNotFoundException
	for _, parent := range []string{"123456789012:s3tablescatalog/absent", "other"} {
		_, aerr := s.getCatalogsTyped(ctx, &getCatalogsReq{ParentCatalogId: parent})
		wantCode(t, "GetCatalogs("+parent+")", aerr, codeEntityNotFound)
	}
}

func TestGetCatalogs_withoutS3TablesListsOnlyTheRoot(t *testing.T) {
	// Given: a service with no S3 Tables wired
	s, _, _ := newTestService(t)

	// When
	out, aerr := s.getCatalogsTyped(context.Background(), &getCatalogsReq{IncludeRoot: ptr(true), Recursive: true})

	// Then
	mustOK(t, "GetCatalogs", aerr)
	if got := catalogNames(out.CatalogList); got != "123456789012" {
		t.Errorf("catalogs = %q", got)
	}
}

func TestFederatedReads_serveNamespacesAsDatabasesAndTablesAsTables(t *testing.T) {
	s := newFederatedService(t)
	ctx := context.Background()

	// When / Then: the bucket's namespaces are its catalog's databases
	dbs, aerr := s.getDatabasesTyped(ctx, &getDatabasesReq{catalogRef: catalogRef{lakeCatalogID}})
	mustOK(t, "GetDatabases", aerr)
	if len(dbs.DatabaseList) != 1 || dbs.DatabaseList[0].Name != "sales" || dbs.DatabaseList[0].CatalogId != lakeCatalogID {
		t.Fatalf("GetDatabases = %+v", dbs.DatabaseList)
	}
	db, aerr := s.getDatabaseTyped(ctx, &getDatabaseReq{catalogRef: catalogRef{"s3tablescatalog/lake"}, Name: "SALES"})
	mustOK(t, "GetDatabase", aerr)
	if db.Database.Name != "sales" || db.Database.CreateTime != epochSeconds(fakeCreated) {
		t.Errorf("GetDatabase = %+v", db.Database)
	}

	// And: its tables are Iceberg tables pointing at their metadata
	tbl, aerr := s.getTableTyped(ctx, &getTableReq{catalogRef: catalogRef{lakeCatalogID}, DatabaseName: "sales", Name: "orders"})
	mustOK(t, "GetTable", aerr)
	got := tbl.Table
	if got.CatalogId != lakeCatalogID || got.DatabaseName != "sales" || got.TableType != "EXTERNAL_TABLE" ||
		got.Parameters["table_type"] != "ICEBERG" || got.Parameters["metadata_location"] != "s3://w--table-s3/metadata/00000-a.metadata.json" ||
		got.StorageDescriptor.Location != "s3://w--table-s3" || len(got.StorageDescriptor.Columns) != 2 ||
		got.StorageDescriptor.Columns[1].Type != "decimal(10,2)" || got.UpdateTime != epochSeconds(fakeCreated.Add(time.Hour)) {
		t.Errorf("GetTable = %+v, sd %+v", got, got.StorageDescriptor)
	}

	// And: GetTables filters by Expression
	tables, aerr := s.getTablesTyped(ctx, &getTablesReq{catalogRef: catalogRef{lakeCatalogID}, DatabaseName: "sales", Expression: "ref.*"})
	mustOK(t, "GetTables", aerr)
	if len(tables.TableList) != 1 || tables.TableList[0].Name != "refunds" {
		t.Errorf("GetTables = %+v", tables.TableList)
	}
	if _, ok := tables.TableList[0].Parameters["metadata_location"]; ok {
		t.Error("a table with no metadata yet has a metadata_location")
	}
}

func TestFederatedReads_answerEntityNotFound(t *testing.T) {
	s := newFederatedService(t)
	ctx := context.Background()

	// A catalog under s3tablescatalog that Overcast does not have is not
	// read as the account's own
	for _, id := range []string{"123456789012:s3tablescatalog/absent", "s3tablescatalog/", "s3tablescatalog/lake/x", "999999999999:s3tablescatalog/lake"} {
		_, aerr := s.getDatabasesTyped(ctx, &getDatabasesReq{catalogRef: catalogRef{id}})
		wantCode(t, "GetDatabases("+id+")", aerr, codeEntityNotFound)
	}
	_, aerr := s.getDatabaseTyped(ctx, &getDatabaseReq{catalogRef: catalogRef{"999999999999:s3tablescatalog"}, Name: "sales"})
	wantCode(t, "GetDatabase(other account)", aerr, codeEntityNotFound)
	_, aerr = s.getDatabaseTyped(ctx, &getDatabaseReq{catalogRef: catalogRef{lakeCatalogID}, Name: "absent"})
	wantCode(t, "GetDatabase(absent)", aerr, codeEntityNotFound)
	_, aerr = s.getTableTyped(ctx, &getTableReq{catalogRef: catalogRef{lakeCatalogID}, DatabaseName: "sales", Name: "absent"})
	wantCode(t, "GetTable(absent)", aerr, codeEntityNotFound)

	// And: s3tablescatalog itself holds catalogs, not databases
	dbs, aerr := s.getDatabasesTyped(ctx, &getDatabasesReq{catalogRef: catalogRef{"s3tablescatalog"}})
	mustOK(t, "GetDatabases(s3tablescatalog)", aerr)
	if dbs.DatabaseList == nil || len(dbs.DatabaseList) != 0 {
		t.Errorf("GetDatabases(s3tablescatalog) = %#v", dbs.DatabaseList)
	}
}

func TestFederatedCatalog_refusesEveryOtherOperation(t *testing.T) {
	// Given: a Glue service with S3 Tables federated
	s := newFederatedService(t)
	h := http.HandlerFunc(s.Dispatch)

	for _, id := range []string{lakeCatalogID, "s3tablescatalog/", "999999999999:s3tablescatalog/lake"} {
		for _, target := range []string{"CreateDatabase", "CreateTable", "GetPartitions", "UpdateTable", "DeleteDatabase"} {
			// When: an operation other than a database or table read names a catalog under s3tablescatalog
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"CatalogId":"`+id+`","DatabaseInput":{"Name":"x"},"DatabaseName":"sales","Name":"x","TableInput":{"Name":"x"}}`))
			req.Header.Set("X-Amz-Target", "AWSGlue."+target)
			req.Header.Set("Content-Type", "application/x-amz-json-1.1")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			// Then: it is not implemented, rather than applied to the account's catalog
			if rec.Code != http.StatusNotImplemented {
				t.Errorf("%s on %s: status %d: %s", target, id, rec.Code, rec.Body)
			}
		}
	}
	if _, found, _ := s.store.getDatabase(context.Background(), "x"); found {
		t.Error("a write to the federated catalog landed in the account's own")
	}
}

func TestCatalogs_resolveTheDefaultAndABucketsCatalog(t *testing.T) {
	s := newFederatedService(t)
	ctx := context.Background()
	mustOK(t, "CreateDatabase", func() *protocol.AWSError {
		_, aerr := s.createDatabaseTyped(ctx, &createDatabaseReq{DatabaseInput: &DatabaseInput{Name: "local"}})
		return aerr
	}())
	cs := s.Catalogs()

	// The default catalog is the account's own
	if _, found, _ := cs.Default().GetDatabase(ctx, "local"); !found {
		t.Error("Default() does not read the account's catalog")
	}

	// A bucket's catalog reads its namespaces
	cat, found, err := cs.Resolve(ctx, lakeCatalogID)
	if err != nil || !found {
		t.Fatalf("Resolve(lake) = %v, %v", found, err)
	}
	if _, found, _ := cat.GetDatabase(ctx, "sales"); !found {
		t.Error("the bucket's catalog does not read its namespace")
	}

	// A bucket that does not exist is not found
	if _, found, err := cs.Resolve(ctx, "123456789012:s3tablescatalog/absent"); found || err != nil {
		t.Errorf("Resolve(absent) = %v, %v", found, err)
	}
}
