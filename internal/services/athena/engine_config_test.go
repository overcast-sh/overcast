package athena

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/services/glue"
)

func TestRenderEngineFiles_heapFollowsTheMemoryLimit(t *testing.T) {
	cases := []struct {
		memory                    int64
		xmx, perNode, total, room string
	}{
		// The plan's measured recipe at the default 1 GiB limit.
		{config.DefaultAthenaEngineMemory, "-Xmx512m", "query.max-memory-per-node=128MB", "query.max-memory=256MB", "memory.heap-headroom-per-node=64MB"},
		// A smaller limit never goes below the 512 MiB floor.
		{256 << 20, "-Xmx512m", "query.max-memory-per-node=128MB", "query.max-memory=256MB", "memory.heap-headroom-per-node=64MB"},
		// A larger one gives the heap half of it.
		{4 << 30, "-Xmx2048m", "query.max-memory-per-node=512MB", "query.max-memory=1024MB", "memory.heap-headroom-per-node=256MB"},
	}
	for _, c := range cases {
		files := renderEngineFiles(engineSettings{Overcast: "http://o:1", Region: "eu-west-1", AccountID: "1", Memory: c.memory})
		props := files["config.properties"]
		if !strings.Contains(files["jvm.config"], c.xmx+"\n") || !strings.Contains(props, c.perNode+"\n") ||
			!strings.Contains(props, c.total+"\n") || !strings.Contains(props, c.room+"\n") {
			t.Errorf("memory %d: jvm.config %q, config.properties %q", c.memory, files["jvm.config"], props)
		}
	}
}

func TestRenderEngineFiles_catalogsPointAtOvercast(t *testing.T) {
	files := renderEngineFiles(engineSettings{Overcast: "http://gw:9", Region: "eu-west-1", AccountID: "111122223333", Memory: 1 << 30})
	for _, name := range []string{"catalog/awsdatacatalog.properties", "catalog/awsdatacatalog_iceberg.properties"} {
		for _, want := range []string{"hive.metastore.glue.endpoint-url=http://gw:9", "s3.endpoint=http://gw:9",
			"hive.metastore.glue.region=eu-west-1", "hive.metastore.glue.catalogid=111122223333", "s3.path-style-access=true"} {
			if !strings.Contains(files[name], want+"\n") {
				t.Errorf("%s lacks %q:\n%s", name, want, files[name])
			}
		}
	}
	if !strings.Contains(files["catalog/awsdatacatalog.properties"], "hive.iceberg-catalog-name=awsdatacatalog_iceberg\n") {
		t.Error("the Hive catalog does not redirect Iceberg tables")
	}
}

func TestEncodeCSV_quotesAsAthenaDoes(t *testing.T) {
	quoted, empty := `say "hi", ok`, ""
	got := string(encodeCSV([][]*string{textRow("a", "b", "c"), {&quoted, nil, &empty}}))
	if want := "\"a\",\"b\",\"c\"\n\"say \"\"hi\"\", ok\",,\"\"\n"; got != want {
		t.Fatalf("encodeCSV = %q, want %q", got, want)
	}
}

func TestEngineStatus_reportsTheInertEngine(t *testing.T) {
	// Given: Athena with ATHENA_ENGINE=inert
	s, _ := newTestService(t)
	s.cfg.AthenaEngine = config.AthenaEngineInert
	s.engine = nil

	// When: the status endpoint is read
	rec := httptest.NewRecorder()
	s.serveEngineStatus(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, engineStatusPath, nil))

	// Then: it says the engine is off, and why
	var st engineStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (%v)", rec.Code, rec.Body, err)
	}
	if st.Engine != "inert" || st.State != engineOff || !strings.Contains(st.Reason, "ATHENA_ENGINE=inert") {
		t.Fatalf("status = %+v", st)
	}
}

func TestRunnerFor_routesEachStatement(t *testing.T) {
	// Given: Athena whose engine is available, and an Iceberg and a Hive table
	ctx := context.Background()
	s, _ := newCatalogService(t)
	s.engine = readyEngine(t, "http://engine.test:1")
	mustRun(t, s, "CREATE DATABASE demo")
	mustRun(t, s, "CREATE EXTERNAL TABLE demo.hive_t (a int) LOCATION 's3://b/h/'")
	if aerr := s.catalogWriter.CreateTable(ctx, "demo", icebergTableInput("ice_t")); aerr != nil {
		t.Fatal(aerr)
	}
	qe := func(query string) QueryExecution {
		return QueryExecution{QueryExecutionId: "id", Query: query, StatementType: statementType(query),
			QueryExecutionContext: QueryExecutionContext{Database: "demo"}}
	}

	// Then: DDL goes to the catalog, DROP of an Iceberg table and everything
	// else to the engine, and a malformed DDL statement fails
	if _, ok := s.runnerFor(ctx, qe("SHOW TABLES")).(ddlRunner); !ok {
		t.Error("SHOW TABLES did not go to the catalog")
	}
	if _, ok := s.runnerFor(ctx, qe("DROP TABLE hive_t")).(ddlRunner); !ok {
		t.Error("DROP of a Hive table did not go to the catalog")
	}
	if r, ok := s.runnerFor(ctx, qe("DROP TABLE ice_t")).(trinoRunner); !ok || r.sql != `DROP TABLE "awsdatacatalog_iceberg"."demo"."ice_t"` {
		t.Errorf("DROP of an Iceberg table = %+v", r)
	}
	if r, ok := s.runnerFor(ctx, qe("SELECT 1")).(trinoRunner); !ok || !r.header || r.session.Schema != "demo" {
		t.Errorf("SELECT = %+v", r)
	}
	if _, ok := s.runnerFor(ctx, qe("DROP TABLE")).(failedRunner); !ok {
		t.Error("a malformed DDL statement did not fail")
	}

	// And: with no engine, the engine's statements run inert
	s.engine = nil
	if _, ok := s.runnerFor(ctx, qe("SELECT 1")).(inertRunner); !ok {
		t.Error("SELECT without an engine did not run inert")
	}
}

// icebergTableInput is an Iceberg table as its Glue catalog records it.
func icebergTableInput(name string) glue.TableInput {
	return glue.TableInput{Name: name, TableType: "EXTERNAL_TABLE", Parameters: map[string]string{"table_type": "ICEBERG"}}
}
