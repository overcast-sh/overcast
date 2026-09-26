package athena

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/overcast-sh/overcast/internal/events"
)

func TestGlueCatalogs_pointAtOvercastThroughTheGateway(t *testing.T) {
	// Given: an engine whose gateway is http://gw:9
	s := engineSettings{Overcast: "http://gw:9", AccessKey: "OVERCASTATHENAKEY", Region: "eu-west-1", AccountID: "111122223333"}

	// When: its Glue catalogs are made
	catalogs := glueCatalogs(s)

	// Then: both reach Glue and S3 at the gateway, signed with its key
	if len(catalogs) != 2 || catalogs[0].name != hiveCatalog || catalogs[0].connector != "hive" ||
		catalogs[1].name != icebergCatalog || catalogs[1].connector != "iceberg" {
		t.Fatalf("catalogs = %+v", catalogs)
	}
	for _, c := range catalogs {
		for k, want := range map[string]string{
			"hive.metastore.glue.endpoint-url": "http://gw:9", "s3.endpoint": "http://gw:9",
			"hive.metastore.glue.region": "eu-west-1", "hive.metastore.glue.catalogid": "111122223333",
			"s3.path-style-access": "true", "hive.metastore.glue.aws-access-key": "OVERCASTATHENAKEY",
			"s3.aws-access-key": "OVERCASTATHENAKEY",
		} {
			if got := c.properties[k]; got != want {
				t.Errorf("%s: %s = %q, want %q", c.name, k, got, want)
			}
		}
	}
	// And: the Hive catalog hands Iceberg tables to the Iceberg one
	if catalogs[0].properties["hive.iceberg-catalog-name"] != icebergCatalog || catalogs[1].properties["iceberg.catalog.type"] != "glue" {
		t.Errorf("catalogs = %+v", catalogs)
	}
}

func TestS3TablesCatalog_isTheBucketsRESTCatalogSignedForS3Tables(t *testing.T) {
	// Given: a table bucket
	s := engineSettings{Overcast: "http://gw:9", AccessKey: "OVERCASTATHENAKEY", Region: "eu-west-1", AccountID: "111122223333"}
	bucket := events.S3TableBucket{Name: "sales-data", ARN: "arn:aws:s3tables:eu-west-1:111122223333:bucket/sales-data"}

	// When: its catalog is made
	c := s3TablesCatalog(s, bucket)

	// Then: it is named as Athena names it, and is the bucket's warehouse on
	// the REST catalog at the gateway, signed with the gateway's key
	if c.name != "s3tablescatalog/sales-data" || c.connector != "iceberg" {
		t.Fatalf("catalog = %+v", c)
	}
	for k, want := range map[string]string{
		"iceberg.catalog.type": "rest", "iceberg.rest-catalog.uri": "http://gw:9/iceberg",
		"iceberg.rest-catalog.warehouse": bucket.ARN, "iceberg.rest-catalog.security": "SIGV4",
		"iceberg.rest-catalog.signing-name": "s3tables", "iceberg.rest-catalog.view-endpoints-enabled": "false",
		"s3.aws-access-key": "OVERCASTATHENAKEY", "s3.region": "eu-west-1", "s3.endpoint": "http://gw:9",
	} {
		if got := c.properties[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// And: it carries nothing of the Glue metastore, which the Iceberg REST
	// connector would refuse as an unused property
	for k := range c.properties {
		if strings.HasPrefix(k, "hive.") {
			t.Errorf("carries %s", k)
		}
	}
}

func TestCreateCatalogSQL_quotesEveryNameAndValue(t *testing.T) {
	got := createCatalogSQL(engineCatalog{name: "s3tablescatalog/b", connector: "iceberg",
		properties: map[string]string{"b.key": "it's", "a.key": "v"}})
	want := `CREATE CATALOG IF NOT EXISTS "s3tablescatalog/b" USING iceberg WITH ("a.key" = 'v', "b.key" = 'it''s')`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := dropCatalogSQL("s3tablescatalog/b"); got != `DROP CATALOG IF EXISTS "s3tablescatalog/b"` {
		t.Errorf("drop = %s", got)
	}
}

// fakeTableBuckets is the table buckets S3 Tables has.
type fakeTableBuckets struct {
	events.S3TablesCatalog // only ListTableBuckets is called

	mu      sync.Mutex
	buckets []events.S3TableBucket
	err     error
}

func (f *fakeTableBuckets) set(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets = nil
	for _, n := range names {
		f.buckets = append(f.buckets, events.S3TableBucket{Name: n, ARN: "arn:aws:s3tables:us-east-1:123456789012:bucket/" + n})
	}
}

func (f *fakeTableBuckets) ListTableBuckets(context.Context) ([]events.S3TableBucket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.buckets), f.err
}

// syncedEngine is a running engine whose table buckets are names.
func syncedEngine(t *testing.T, names ...string) (*engineManager, *fakeTrino, *fakeTableBuckets) {
	t.Helper()
	f := newFakeTrino(t)
	m := readyEngine(t, f.srv.URL)
	tables := &fakeTableBuckets{}
	tables.set(names...)
	m.tables = tables
	return m, f, tables
}

// catalogStatements are the statements that created or dropped a catalog,
// each as its verb and the catalog's name.
func catalogStatements(statements []string) []string {
	var out []string
	for _, s := range statements {
		for verb, prefix := range map[string]string{"CREATE": "CREATE CATALOG IF NOT EXISTS ", "DROP": "DROP CATALOG IF EXISTS "} {
			if rest, ok := strings.CutPrefix(s, prefix); ok {
				name, _, _ := strings.Cut(rest, " ")
				out = append(out, verb+" "+name)
			}
		}
	}
	return out
}

func TestSyncS3TablesCatalogs_followsTheTableBuckets(t *testing.T) {
	// Given: a running engine and two table buckets
	m, f, tables := syncedEngine(t, "alpha", "beta")
	ctx := context.Background()

	// When: a query runs
	m.syncS3TablesCatalogs(ctx, f.srv.URL)

	// Then: the engine is given a catalog for each
	got := catalogStatements(f.statements())
	slices.Sort(got)
	if want := []string{`CREATE "s3tablescatalog/alpha"`, `CREATE "s3tablescatalog/beta"`}; !slices.Equal(got, want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}

	// When: another query runs with nothing changed
	m.syncS3TablesCatalogs(ctx, f.srv.URL)

	// Then: nothing is created again
	if n := len(f.statements()); n != 2 {
		t.Fatalf("ran %d statements, want 2: %q", n, f.statements())
	}

	// When: one bucket is deleted and another created
	tables.set("beta", "gamma")
	m.syncS3TablesCatalogs(ctx, f.srv.URL)

	// Then: the gone bucket's catalog is dropped and the new one's created
	got = catalogStatements(f.statements()[2:])
	slices.Sort(got)
	if want := []string{`CREATE "s3tablescatalog/gamma"`, `DROP "s3tablescatalog/alpha"`}; !slices.Equal(got, want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}
}

func TestSyncS3TablesCatalogs_retriesACatalogTheEngineRefused(t *testing.T) {
	// Given: an engine that refuses the first try at a bucket's catalog
	m, f, tables := syncedEngine(t, "alpha")
	create := createCatalogSQL(s3TablesCatalog(m.boot.settings, tables.buckets[0]))
	f.script(create, trinoResponse{Error: &trinoError{ErrorName: "ICEBERG_CATALOG_ERROR", Message: "Cannot obtain metadata"}})

	// When: a query runs, and then another once the engine accepts it
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)
	f.script(create, selectOne()...)
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)

	// Then: the second query tried again, and a third has nothing to do
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)
	if got := f.statements(); len(got) != 2 || got[0] != create || got[1] != create {
		t.Fatalf("statements = %q", got)
	}
}

func TestSyncS3TablesCatalogs_leavesAReplacedEngineAlone(t *testing.T) {
	// Given: a query that acquired an engine since replaced
	m, f, _ := syncedEngine(t, "alpha")

	// When: it syncs the catalogs of the engine it had
	m.syncS3TablesCatalogs(context.Background(), "http://gone.test:1")

	// Then: nothing runs anywhere
	if got := f.statements(); len(got) != 0 {
		t.Fatalf("statements = %q", got)
	}
}

func TestSyncS3TablesCatalogs_retriesADropTheEngineRefused(t *testing.T) {
	// Given: an engine with a catalog for a bucket since deleted, which
	// refuses the first try at dropping it
	m, f, tables := syncedEngine(t, "alpha")
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)
	tables.set()
	drop := dropCatalogSQL(s3TablesCatalogName("alpha"))
	f.script(drop, trinoResponse{Error: &trinoError{ErrorName: "GENERIC_INTERNAL_ERROR", Message: "busy"}})

	// When: a query runs, and then another once the engine accepts it
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)
	f.script(drop, selectOne()...)
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)

	// Then: the second query tried again, and a third has nothing to do
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)
	if got := f.statements(); len(got) != 3 || got[1] != drop || got[2] != drop {
		t.Fatalf("statements = %q", got)
	}
}

func TestSyncS3TablesCatalogs_leavesTheCatalogsAloneWhenBucketsCannotBeListed(t *testing.T) {
	// Given: an engine with a bucket's catalog, and S3 Tables failing to list
	m, f, tables := syncedEngine(t, "alpha")
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)
	tables.mu.Lock()
	tables.buckets, tables.err = nil, errors.New("store unavailable")
	tables.mu.Unlock()

	// When: a query runs
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)

	// Then: nothing is dropped for want of a list
	if got := f.statements(); len(got) != 1 {
		t.Fatalf("statements = %q", got)
	}
}

func TestSyncS3TablesCatalogs_withoutS3TablesDoesNothing(t *testing.T) {
	// Given: an engine S3 Tables was never wired to
	f := newFakeTrino(t)
	m := readyEngine(t, f.srv.URL)

	// When: a query runs
	m.syncS3TablesCatalogs(context.Background(), f.srv.URL)

	// Then: no catalog is made
	if got := f.statements(); len(got) != 0 {
		t.Fatalf("statements = %q", got)
	}
}

func TestSyncS3TablesCatalogs_concurrentQueriesCreateEachCatalogOnce(t *testing.T) {
	// Given: an engine and three table buckets
	m, f, _ := syncedEngine(t, "alpha", "beta", "gamma")

	// When: eight queries sync at once
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { m.syncS3TablesCatalogs(context.Background(), f.srv.URL) })
	}
	wg.Wait()

	// Then: each catalog was created once
	if got := catalogStatements(f.statements()); len(got) != 3 {
		t.Fatalf("statements = %q, want one CREATE per bucket", got)
	}
}

func TestTrinoRunner_syncsTheCatalogsOnlyForATableBucketStatement(t *testing.T) {
	// Given: an engine and a table bucket
	m, f, _ := syncedEngine(t, "alpha")

	// When: a statement about AwsDataCatalog runs, then one about the bucket
	for _, s3Tables := range []bool{false, true} {
		r := testRunner(m, "SELECT 1")
		r.s3Tables = s3Tables
		if _, fail := r.run(context.Background(), func() {}, &collectedRows{}); fail != nil {
			t.Fatalf("run: %+v", fail)
		}
	}

	// Then: only the second made the bucket's catalog, before it ran
	if got := f.statements(); len(got) != 3 || got[0] != "SELECT 1" || !strings.HasPrefix(got[1], "CREATE CATALOG") || got[2] != "SELECT 1" {
		t.Fatalf("statements = %q", got)
	}
}
