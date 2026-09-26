package samples_test

// samples_test.go — the analytics sample dataset, loaded the two ways it can
// be: by the loader over the network (what `overcast samples load` runs)
// and by POST /_overcast/samples/{dataset} (what the console calls). Both
// are checked through the AWS SDK for Go v2, on the inert engine.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/overcast-sh/overcast/internal/athenaquery"
	"github.com/overcast-sh/overcast/internal/samples"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
	athenasvc "github.com/overcast-sh/overcast/internal/services/athena"
	"github.com/overcast-sh/overcast/tests/helpers"
)

const (
	bucket   = "overcast-sample-analytics"
	database = "sample_analytics"
)

type env struct {
	t    *testing.T
	srv  *helpers.TestServer
	cfg  aws.Config
	glue *glue.Client
	s3   *s3.Client
}

func newEnv(t *testing.T) *env {
	srv := helpers.NewTestServer(t)
	cfg := sdkconfig.ForEndpoint(srv.URL, "us-east-1", http.DefaultClient)
	return &env{t: t, srv: srv, cfg: cfg, glue: glue.NewFromConfig(cfg), s3: s3.NewFromConfig(cfg, sdkconfig.PathStyle)}
}

func (e *env) load() *samples.Report {
	e.t.Helper()
	engine := func(ctx context.Context) (athenasvc.EngineStatus, error) {
		return athenaquery.FetchEngineStatus(ctx, http.DefaultClient, e.srv.URL)
	}
	report, err := samples.NewLoader(e.cfg, engine).Load(e.t.Context(), "analytics")
	if err != nil {
		e.t.Fatalf("load: %v", err)
	}
	return report
}

func (e *env) post(dataset string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodPost, e.srv.URL+"/_overcast/samples/"+dataset, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// assertLoaded checks the catalog and the bucket hold the dataset.
func (e *env) assertLoaded() {
	t := e.t
	t.Helper()
	tables, err := e.glue.GetTables(t.Context(), &glue.GetTablesInput{DatabaseName: aws.String(database)})
	if err != nil {
		t.Fatalf("GetTables: %v", err)
	}
	formats := map[string]string{}
	for _, tb := range tables.TableList {
		formats[aws.ToString(tb.Name)] = aws.ToString(tb.StorageDescriptor.SerdeInfo.SerializationLibrary)
	}
	if len(formats) != 2 || formats["orders_csv"] != "org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe" ||
		formats["orders_parquet"] != "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe" {
		t.Fatalf("tables = %v", formats)
	}
	parts, err := e.glue.GetPartitions(t.Context(), &glue.GetPartitionsInput{DatabaseName: aws.String(database), TableName: aws.String("orders_csv")})
	if err != nil || len(parts.Partitions) != 3 {
		t.Fatalf("partitions = %v, err = %v; want one per region", parts, err)
	}
	objects, err := e.s3.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("parquet/")})
	if err != nil || len(objects.Contents) != 1 || aws.ToString(objects.Contents[0].Key) != "parquet/orders/orders.parquet" {
		t.Fatalf("parquet objects = %v, err = %v", objects, err)
	}
}

func TestLoadAnalytics_overTheNetworkIsIdempotent(t *testing.T) {
	// Given: an emulator with the inert engine
	e := newEnv(t)

	// When: the dataset is loaded twice
	e.load()
	report := e.load()

	// Then: the files and tables are there once, and the report says why
	// there is no Iceberg copy
	e.assertLoaded()
	if report.Bucket != bucket || report.Database != database || len(report.Tables) != 2 ||
		report.Tables[0].Partitions != 3 || report.IcebergSkipped == "" {
		t.Fatalf("report = %+v", report)
	}
}

func TestSamplesEndpoint_loadsTheDataset(t *testing.T) {
	// Given: an emulator
	e := newEnv(t)

	// When: the console's endpoint is called
	resp := e.post("analytics")
	defer resp.Body.Close()

	// Then: it answers with the report, and the dataset is loaded
	helpers.AssertStatus(t, resp, http.StatusOK)
	var report samples.Report
	helpers.DecodeJSON(t, resp, &report)
	if report.Dataset != "analytics" || len(report.Tables) != 2 {
		t.Fatalf("report = %+v", report)
	}
	e.assertLoaded()
}

func TestSamplesEndpoint_unknownDataset(t *testing.T) {
	e := newEnv(t)

	resp := e.post("nope")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
	var body map[string]string
	helpers.DecodeJSON(t, resp, &body)
	if body["error"] == "" {
		t.Fatalf("body = %v", body)
	}
}

func TestLoadAnalytics_resetRemovesIt(t *testing.T) {
	// Given: a loaded dataset
	e := newEnv(t)
	e.load()

	// When: athena, glue and s3 are reset
	for _, svc := range []string{"athena", "glue", "s3"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+"/_overcast/reset/"+svc, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
	}

	// Then: the database and the bucket are gone
	_, err := e.glue.GetDatabase(t.Context(), &glue.GetDatabaseInput{Name: aws.String(database)})
	var noDatabase *gluetypes.EntityNotFoundException
	if !errors.As(err, &noDatabase) {
		t.Fatalf("GetDatabase after reset: %v", err)
	}
	_, err = e.s3.HeadBucket(t.Context(), &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	var noBucket *s3types.NotFound
	if !errors.As(err, &noBucket) {
		t.Fatalf("HeadBucket after reset: %v", err)
	}
}

func TestSamplesEndpoint_reportJSON(t *testing.T) {
	// The console reads these field names.
	e := newEnv(t)
	resp := e.post("analytics")
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	helpers.DecodeJSON(t, resp, &raw)
	for _, key := range []string{"dataset", "bucket", "database", "tables", "icebergSkipped"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("report %v is missing %q", raw, key)
		}
	}
}

func TestSamplesEndpoint_refusedWithoutAthena(t *testing.T) {
	// Given: an emulator with Glue and S3 but no Athena
	srv := helpers.NewTestServer(t, helpers.WithServiceSubset("glue", "s3"))
	e := &env{t: t, srv: srv}

	// When: the dataset is asked for
	resp := e.post("analytics")
	defer resp.Body.Close()

	// Then: it is refused, naming what is missing, and nothing is created
	helpers.AssertStatus(t, resp, http.StatusConflict)
	var body map[string]string
	helpers.DecodeJSON(t, resp, &body)
	if !strings.Contains(body["error"], "not enabled: athena") {
		t.Fatalf("body = %v", body)
	}
}
