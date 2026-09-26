package athena

import (
	"context"
	"encoding/json"
	"maps"
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

func TestRenderEngineFiles_catalogsAreDynamic(t *testing.T) {
	// The catalogs are created once the engine is up, so none is a file.
	files := renderEngineFiles(engineSettings{Overcast: "http://o:1", Region: "eu-west-1", AccountID: "1", Memory: 1 << 30})
	for _, want := range []string{"catalog.management=dynamic\n", "catalog.store=memory\n"} {
		if !strings.Contains(files["config.properties"], want) {
			t.Errorf("config.properties lacks %q:\n%s", want, files["config.properties"])
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "catalog/") {
			t.Errorf("rendered a catalog file %s", name)
		}
	}
}

func TestRenderEngineFiles_leavesThePluginDirectoryAlone(t *testing.T) {
	// The engine loads whatever the image installs under its plugin
	// directory — only hive and iceberg in the default image — so nothing
	// redirects Trino elsewhere.
	files := renderEngineFiles(engineSettings{Overcast: "http://o:1", Region: "eu-west-1", AccountID: "1", Memory: 1 << 30})
	if strings.Contains(files["config.properties"], "plugin.dir=") {
		t.Errorf("config.properties overrides the plugin directory:\n%s", files["config.properties"])
	}
}

func TestEngineConfigArchive_holdsOnlyTheRenderedFiles(t *testing.T) {
	// Given: two rendered files
	files := map[string]string{"node.properties": "a=1\n", "catalog/c.properties": "b=2\n"}

	// When: they are archived for the container
	archive, err := engineConfigArchive(files)
	if err != nil {
		t.Fatal(err)
	}

	// Then: the archive holds each of them under engineEtcDir, and nothing
	// else — untar fails on any entry that is not a regular file
	want := map[string]string{"etc/athena/node.properties": "a=1\n", "etc/athena/catalog/c.properties": "b=2\n"}
	if got := untar(t, archive); !maps.Equal(got, want) {
		t.Errorf("archive = %q, want %q", got, want)
	}
}

func TestEngineStatus_reportsTheInertEngine(t *testing.T) {
	// Given: Athena with ATHENA_ENGINE=inert
	s, _ := newTestService(t)
	s.cfg.AthenaEngine = config.AthenaEngineInert
	s.engine = nil

	// When: the status endpoint is read
	rec := httptest.NewRecorder()
	s.serveEngineStatus(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, EngineStatusPath, nil))

	// Then: it says the engine is off, and why
	var st EngineStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s (%v)", rec.Code, rec.Body, err)
	}
	if st.Engine != "inert" || st.State != EngineOff || !strings.Contains(st.Reason, "ATHENA_ENGINE=inert") {
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
