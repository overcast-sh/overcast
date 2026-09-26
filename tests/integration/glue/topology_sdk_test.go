package glue_test

// topology_sdk_test.go — the data lake on the system map, through the real
// router: Glue, S3 Tables and Athena each contribute their part, and the
// edges between them resolve to the other services' nodes.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/glue/types"

	"github.com/overcast-sh/overcast/internal/topology"
	"github.com/overcast-sh/overcast/tests/helpers"
)

func fetchTopology(t *testing.T, srv *helpers.TestServer) topology.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + "/_overcast/topology")
	if err != nil {
		t.Fatalf("GET /_overcast/topology: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out topology.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode topology: %v", err)
	}
	return out
}

func TestTopology_dataLakeNodesAndEdges(t *testing.T) {
	// Given: a table bucket with an Iceberg table, an S3 bucket a Glue
	// table's data is in, and a query that read that table's database and
	// wrote its results to the same bucket
	srv := helpers.NewTestServer(t)
	ctx := context.Background()
	helpers.SeedS3Table(t, srv, "lake", "sales", "orders")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/raw", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CreateBucket raw: %v %v", resp, err)
	}
	c := glueClient(t, srv)
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(c.CreateDatabase(ctx, &glue.CreateDatabaseInput{DatabaseInput: &types.DatabaseInput{Name: aws.String("web")}}))
	must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String("web"), TableInput: &types.TableInput{
		Name:              aws.String("clicks"),
		StorageDescriptor: &types.StorageDescriptor{Location: aws.String("s3://raw/clicks/")},
		Parameters:        map[string]string{"classification": "csv"},
	}}))
	a := athena.New(athena.Options{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL), HTTPClient: http.DefaultClient,
	})
	if _, err := a.StartQueryExecution(ctx, &athena.StartQueryExecutionInput{
		QueryString:           aws.String("SELECT count(*) FROM clicks"),
		QueryExecutionContext: &athenatypes.QueryExecutionContext{Database: aws.String("web")},
		ResultConfiguration:   &athenatypes.ResultConfiguration{OutputLocation: aws.String("s3://raw/results/")},
	}); err != nil {
		t.Fatalf("StartQueryExecution: %v", err)
	}

	// When: the system map is fetched
	topo := fetchTopology(t, srv)

	// Then: the table's warehouse bucket is not an S3 node; the table bucket
	// lists the table, and the database lists its table with its format
	nodes := make(map[string]topology.Node)
	for _, n := range topo.Nodes {
		nodes[n.ID] = n
		if n.Service == "s3" && strings.HasSuffix(n.Label, "--table-s3") {
			t.Errorf("warehouse bucket drawn as an S3 node: %s", n.ID)
		}
	}
	if lake := nodes["us-east-1::s3tables::lake"]; len(lake.Tables) != 1 || lake.Tables[0].Name != "orders" || lake.Tables[0].Namespace != "sales" {
		t.Errorf("table bucket node = %+v", lake)
	}
	if web := nodes["us-east-1::glue::web"]; len(web.Tables) != 1 || web.Tables[0].Format != "CSV" {
		t.Errorf("database node = %+v", web)
	}
	if primary := nodes["us-east-1::athena::primary"]; len(primary.RecentQueries) != 1 {
		t.Errorf("workgroup node = %+v", primary)
	}

	// And: every data-lake edge resolves to the node it names
	got := make(map[string]string)
	for _, e := range topo.Edges {
		got[e.Type] = e.Source + " → " + e.Target
	}
	for typ, want := range map[string]string{
		"table-location": "us-east-1::glue::web → us-east-1::s3::raw",
		"federation":     "us-east-1::glue-catalog::s3tablescatalog → us-east-1::s3tables::lake",
		"query-results":  "us-east-1::athena::primary → us-east-1::s3::raw",
		"queries":        "us-east-1::athena::primary → us-east-1::glue::web",
	} {
		if got[typ] != want {
			t.Errorf("%s edge = %q, want %q", typ, got[typ], want)
		}
	}
}
