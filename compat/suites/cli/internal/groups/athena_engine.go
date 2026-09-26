package groups

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// AthenaEngine returns the athena-engine group: Hive DDL that writes the Glue
// Data Catalog, then a query over a CSV table in S3 that the engine runs —
// its rows, column types and runtime statistics.
func AthenaEngine() ServiceGroup {
	g := &athenaEngineCliGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"athena-engine:CreateDatabaseStatement":      g.CreateDatabaseStatement,
			"athena-engine:CreateExternalTableStatement": g.CreateExternalTableStatement,
			"athena-engine:SelectFromTable":              g.SelectFromTable,
			"athena-engine:GetQueryRuntimeStatistics":    g.GetQueryRuntimeStatistics,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"athena-engine": g.setup,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"athena-engine": g.teardown,
		},
	}
}

type athenaEngineCliGroup struct{}

// athenaEngineCliQueryWait bounds one query, the first of which waits for the
// engine to be pulled and started.
const athenaEngineCliQueryWait = 4 * time.Minute

func athenaEngineCliBucket(t *harness.TestContext) string { return t.RunID + "-athena-engine" }

// athenaEngineCliDatabase is an identifier Hive DDL and Trino SQL both accept
// unquoted.
func athenaEngineCliDatabase(t *harness.TestContext) string {
	return strings.ReplaceAll(t.RunID, "-", "_") + "_athena_engine"
}

func (g *athenaEngineCliGroup) setup(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", athenaEngineCliBucket(t))
}

func (g *athenaEngineCliGroup) teardown(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "glue", "delete-database", "--name", athenaEngineCliDatabase(t)) //nolint:errcheck
	awscli.Run(t.Endpoint, t.Region, "s3", "rb", "s3://"+athenaEngineCliBucket(t), "--force")         //nolint:errcheck
	return nil
}

// run starts query and waits for it to succeed, returning its id.
func (g *athenaEngineCliGroup) run(ctx context.Context, t *harness.TestContext, query string) (string, error) {
	return runAthenaQuery(ctx, t, query, "--result-configuration", "OutputLocation=s3://"+athenaEngineCliBucket(t)+"/results/")
}

// runAthenaQuery starts query with the further start-query-execution
// arguments args, and waits for it to succeed, returning its id.
func runAthenaQuery(ctx context.Context, t *harness.TestContext, query string, args ...string) (string, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, append([]string{"athena", "start-query-execution", "--query-string", query}, args...)...)
	if err != nil {
		return "", err
	}
	id, _ := out["QueryExecutionId"].(string)
	deadline := time.Now().Add(athenaEngineCliQueryWait)
	for {
		got, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-query-execution", "--query-execution-id", id)
		if err != nil {
			return "", err
		}
		qe, _ := got["QueryExecution"].(map[string]any)
		status, _ := qe["Status"].(map[string]any)
		switch status["State"] {
		case "SUCCEEDED":
			return id, nil
		case "FAILED", "CANCELLED":
			return "", fmt.Errorf("%s: %v: %v", query, status["State"], status["StateChangeReason"])
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s: still unfinished after %s", query, athenaEngineCliQueryWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// queryResultRows is a get-query-results output's rows, each as its
// comma-joined cells.
func queryResultRows(out map[string]any) []string {
	rs, _ := out["ResultSet"].(map[string]any)
	rowsIn, _ := rs["Rows"].([]any)
	var rows []string
	for _, r := range rowsIn {
		row, _ := r.(map[string]any)
		data, _ := row["Data"].([]any)
		var cells []string
		for _, d := range data {
			datum, _ := d.(map[string]any)
			v, _ := datum["VarCharValue"].(string)
			cells = append(cells, v)
		}
		rows = append(rows, strings.Join(cells, ","))
	}
	return rows
}

func (g *athenaEngineCliGroup) CreateDatabaseStatement(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.run(ctx, t, "CREATE DATABASE "+athenaEngineCliDatabase(t)); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-database",
		"--catalog-name", "AwsDataCatalog", "--database-name", athenaEngineCliDatabase(t))
	if err != nil {
		return err
	}
	if db, _ := out["Database"].(map[string]any); db["Name"] != athenaEngineCliDatabase(t) {
		return fmt.Errorf("GetDatabase: got %v", out)
	}
	return nil
}

func (g *athenaEngineCliGroup) CreateExternalTableStatement(ctx context.Context, t *harness.TestContext) error {
	if err := awscli.RunWithStdin(t.Endpoint, t.Region, "1,alice\n2,bob\n",
		"s3", "cp", "-", "s3://"+athenaEngineCliBucket(t)+"/people/part-0.csv"); err != nil {
		return err
	}
	if _, err := g.run(ctx, t, "CREATE EXTERNAL TABLE "+athenaEngineCliDatabase(t)+".people (id int, name string) "+
		"ROW FORMAT DELIMITED FIELDS TERMINATED BY ',' LOCATION 's3://"+athenaEngineCliBucket(t)+"/people/'"); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-table-metadata",
		"--catalog-name", "AwsDataCatalog", "--database-name", athenaEngineCliDatabase(t), "--table-name", "people")
	if err != nil {
		return err
	}
	md, _ := out["TableMetadata"].(map[string]any)
	cols, _ := md["Columns"].([]any)
	if len(cols) != 2 {
		return fmt.Errorf("GetTableMetadata: columns %v", md["Columns"])
	}
	first, _ := cols[0].(map[string]any)
	second, _ := cols[1].(map[string]any)
	if first["Name"] != "id" || second["Type"] != "string" {
		return fmt.Errorf("GetTableMetadata: columns %v", cols)
	}
	return nil
}

func (g *athenaEngineCliGroup) SelectFromTable(ctx context.Context, t *harness.TestContext) error {
	id, err := g.run(ctx, t, "SELECT id, name FROM "+athenaEngineCliDatabase(t)+".people ORDER BY id")
	if err != nil {
		return err
	}
	t.Set("athena_engine_query", id)
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-query-results", "--query-execution-id", id)
	if err != nil {
		return err
	}
	if got := strings.Join(queryResultRows(out), ";"); got != "id,name;1,alice;2,bob" {
		return fmt.Errorf("GetQueryResults: rows %q, want the header then 1,alice and 2,bob", got)
	}
	rs, _ := out["ResultSet"].(map[string]any)
	meta, _ := rs["ResultSetMetadata"].(map[string]any)
	info, _ := meta["ColumnInfo"].([]any)
	var types []string
	for _, c := range info {
		col, _ := c.(map[string]any)
		typ, _ := col["Type"].(string)
		types = append(types, typ)
	}
	if strings.Join(types, ",") != "integer,varchar" {
		return fmt.Errorf("GetQueryResults: column types %v", types)
	}
	return nil
}

func (g *athenaEngineCliGroup) GetQueryRuntimeStatistics(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "athena", "get-query-runtime-statistics",
		"--query-execution-id", t.GetString("athena_engine_query"))
	if err != nil {
		return err
	}
	stats, _ := out["QueryRuntimeStatistics"].(map[string]any)
	rows, _ := stats["Rows"].(map[string]any)
	if rows["OutputRows"] != float64(2) || stats["Timeline"] == nil {
		return fmt.Errorf("GetQueryRuntimeStatistics: got %v", stats)
	}
	return nil
}
