package groups

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// AthenaEngine returns the athena-engine group: Hive DDL that writes the Glue
// Data Catalog, then a query over a CSV table in S3 that the engine runs —
// its rows, column types and runtime statistics.
func AthenaEngine(c *clients.Clients) ServiceGroup {
	g := &athenaEngineGroup{c: c}
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

type athenaEngineGroup struct{ c *clients.Clients }

// athenaEngineQueryWait bounds one query, the first of which waits for the
// engine to be pulled and started.
const athenaEngineQueryWait = 4 * time.Minute

func athenaEngineBucket(t *harness.TestContext) string { return t.RunID + "-athena-engine" }

// athenaEngineDatabase is an identifier Hive DDL and Trino SQL both accept
// unquoted.
func athenaEngineDatabase(t *harness.TestContext) string {
	return strings.ReplaceAll(t.RunID, "-", "_") + "_athena_engine"
}

func (g *athenaEngineGroup) setup(ctx context.Context, t *harness.TestContext) error {
	_, err := g.c.S3().CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(athenaEngineBucket(t))})
	return err
}

func (g *athenaEngineGroup) teardown(ctx context.Context, t *harness.TestContext) error {
	g.c.Glue().DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(athenaEngineDatabase(t))}) //nolint:errcheck
	bucket := aws.String(athenaEngineBucket(t))
	if out, err := g.c.S3().ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket}); err == nil {
		for _, o := range out.Contents {
			g.c.S3().DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: bucket, Key: o.Key}) //nolint:errcheck
		}
	}
	g.c.S3().DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: bucket}) //nolint:errcheck
	return nil
}

// run starts query and waits for it to finish, returning its id; a query
// that does not succeed is the test's failure.
func (g *athenaEngineGroup) run(ctx context.Context, t *harness.TestContext, query string) (string, error) {
	return runAthenaQuery(ctx, g.c, &athena.StartQueryExecutionInput{
		QueryString:         aws.String(query),
		ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String("s3://" + athenaEngineBucket(t) + "/results/")},
	})
}

// runAthenaQuery starts a query and waits for it to finish, returning its
// id; a query that does not succeed is an error.
func runAthenaQuery(ctx context.Context, c *clients.Clients, in *athena.StartQueryExecutionInput) (string, error) {
	query := aws.ToString(in.QueryString)
	out, err := c.Athena().StartQueryExecution(ctx, in)
	if err != nil {
		return "", err
	}
	id := aws.ToString(out.QueryExecutionId)
	deadline := time.Now().Add(athenaEngineQueryWait)
	for {
		resp, err := c.Athena().GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)})
		if err != nil {
			return "", err
		}
		switch st := resp.QueryExecution.Status; st.State {
		case types.QueryExecutionStateSucceeded:
			return id, nil
		case types.QueryExecutionStateFailed, types.QueryExecutionStateCancelled:
			return "", fmt.Errorf("%s: %s: %s", query, st.State, aws.ToString(st.StateChangeReason))
		case types.QueryExecutionStateQueued, types.QueryExecutionStateRunning:
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s: still unfinished after %s", query, athenaEngineQueryWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// queryRows is a query result's rows, each as its comma-joined cells.
func queryRows(out *athena.GetQueryResultsOutput) []string {
	var rows []string
	for _, r := range out.ResultSet.Rows {
		var cells []string
		for _, d := range r.Data {
			cells = append(cells, aws.ToString(d.VarCharValue))
		}
		rows = append(rows, strings.Join(cells, ","))
	}
	return rows
}

func (g *athenaEngineGroup) CreateDatabaseStatement(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.run(ctx, t, "CREATE DATABASE "+athenaEngineDatabase(t)); err != nil {
		return err
	}
	resp, err := g.c.Athena().GetDatabase(ctx, &athena.GetDatabaseInput{CatalogName: aws.String("AwsDataCatalog"), DatabaseName: aws.String(athenaEngineDatabase(t))})
	if err != nil {
		return err
	}
	if aws.ToString(resp.Database.Name) != athenaEngineDatabase(t) {
		return fmt.Errorf("GetDatabase: name %q", aws.ToString(resp.Database.Name))
	}
	return nil
}

func (g *athenaEngineGroup) CreateExternalTableStatement(ctx context.Context, t *harness.TestContext) error {
	if _, err := g.c.S3().PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(athenaEngineBucket(t)), Key: aws.String("people/part-0.csv"), Body: strings.NewReader("1,alice\n2,bob\n"),
	}); err != nil {
		return err
	}
	if _, err := g.run(ctx, t, "CREATE EXTERNAL TABLE "+athenaEngineDatabase(t)+".people (id int, name string) "+
		"ROW FORMAT DELIMITED FIELDS TERMINATED BY ',' LOCATION 's3://"+athenaEngineBucket(t)+"/people/'"); err != nil {
		return err
	}
	resp, err := g.c.Athena().GetTableMetadata(ctx, &athena.GetTableMetadataInput{
		CatalogName: aws.String("AwsDataCatalog"), DatabaseName: aws.String(athenaEngineDatabase(t)), TableName: aws.String("people"),
	})
	if err != nil {
		return err
	}
	if cols := resp.TableMetadata.Columns; len(cols) != 2 || aws.ToString(cols[0].Name) != "id" || aws.ToString(cols[1].Type) != "string" {
		return fmt.Errorf("GetTableMetadata: columns %+v", cols)
	}
	return nil
}

func (g *athenaEngineGroup) SelectFromTable(ctx context.Context, t *harness.TestContext) error {
	id, err := g.run(ctx, t, "SELECT id, name FROM "+athenaEngineDatabase(t)+".people ORDER BY id")
	if err != nil {
		return err
	}
	t.Set("athena_engine_query", id)
	resp, err := g.c.Athena().GetQueryResults(ctx, &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id)})
	if err != nil {
		return err
	}
	if got := strings.Join(queryRows(resp), ";"); got != "id,name;1,alice;2,bob" {
		return fmt.Errorf("GetQueryResults: rows %q, want the header then 1,alice and 2,bob", got)
	}
	if cols := resp.ResultSet.ResultSetMetadata.ColumnInfo; len(cols) != 2 || aws.ToString(cols[0].Type) != "integer" || aws.ToString(cols[1].Type) != "varchar" {
		return fmt.Errorf("GetQueryResults: column info %+v", cols)
	}
	return nil
}

func (g *athenaEngineGroup) GetQueryRuntimeStatistics(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.c.Athena().GetQueryRuntimeStatistics(ctx, &athena.GetQueryRuntimeStatisticsInput{
		QueryExecutionId: aws.String(t.GetString("athena_engine_query")),
	})
	if err != nil {
		return err
	}
	stats := resp.QueryRuntimeStatistics
	if stats == nil || stats.Rows == nil || aws.ToInt64(stats.Rows.OutputRows) != 2 || stats.Timeline == nil {
		return fmt.Errorf("GetQueryRuntimeStatistics: %+v", stats)
	}
	return nil
}
