package providers

// runtime_provider_datalake.go — the runtime tools an agent checks a data
// stack with: run an Athena query, describe a Glue table, list an S3 Tables
// table's Iceberg snapshots. Each drives the emulator through its AWS API
// with SDK clients served in process by the router (see SetRouter), so a
// tool answers exactly what the wire API would.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/internal/athenaquery"
	"github.com/overcast-sh/overcast/internal/mcp"
	"github.com/overcast-sh/overcast/internal/sdkconfig"
)

// Bounds on runtime_athena_run_query's wait and row count.
const (
	athenaToolDefaultTimeout = 30 * time.Second
	athenaToolMaxTimeout     = 5 * time.Minute
	athenaToolDefaultRows    = 100
	athenaToolMaxRows        = 1000
)

func (p *RuntimeProvider) dataLakeTools() []mcp.Tool {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []mcp.Tool{
		{
			Name:        "runtime_athena_run_query",
			Description: "Run an Athena SQL query in this running Overcast instance, wait for it (bounded by timeout_seconds), and return the first max_rows rows with the column types, the error when it failed, and the statistics. A query still running when the wait ends is left running and reported with timed_out.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"sql":{"type":"string","description":"The SQL to run."},
					"workgroup":{"type":"string","description":"Workgroup (default primary)."},
					"catalog":{"type":"string","description":"Data catalog (default AwsDataCatalog)."},
					"database":{"type":"string","description":"Database the query runs in."},
					"output_location":{"type":"string","description":"s3:// result location, when the workgroup has none."},
					"max_rows":{"type":"integer","minimum":1,"maximum":1000,"description":"Rows to return (default 100)."},
					"timeout_seconds":{"type":"integer","minimum":1,"maximum":300,"description":"How long to wait for the query (default 30)."}
				},
				"required":["sql"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query_execution_id":{"type":"string"},
					"state":{"type":"string"},
					"state_change_reason":{"type":"string"},
					"statement_type":{"type":"string"},
					"error":{"type":"object"},
					"statistics":{"type":"object"},
					"columns":{"type":"array","items":{"type":"object"}},
					"rows":{"type":"array","items":{"type":"array"}},
					"truncated":{"type":"boolean"},
					"timed_out":{"type":"boolean"},
					"update_count":{"type":"integer"}
				},
				"required":["query_execution_id","state","columns","rows","truncated","timed_out"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			// SQL can create, change and drop tables.
			Execution: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false},
		},
		{
			Name:        "runtime_glue_describe_table",
			Description: "Describe a Glue Data Catalog table in this running Overcast instance: its columns and partition keys, location, format (CSV, JSON, PARQUET, ORC, AVRO, ICEBERG or VIEW) and partition count.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"database":{"type":"string","description":"Database name."},
					"table":{"type":"string","description":"Table name."}
				},
				"required":["database","table"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"database":{"type":"string"},
					"table":{"type":"string"},
					"table_type":{"type":"string"},
					"format":{"type":"string"},
					"location":{"type":"string"},
					"columns":{"type":"array","items":{"type":"object"}},
					"partition_keys":{"type":"array","items":{"type":"object"}},
					"partition_count":{"type":"integer"},
					"parameters":{"type":"object"}
				},
				"required":["database","table","columns","partition_keys","partition_count"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution:   readOnly,
		},
		{
			Name:        "runtime_s3tables_table_snapshots",
			Description: "List the Iceberg snapshots of an S3 Tables table in this running Overcast instance, oldest first, from its current metadata file: id, parent, time, operation and summary, and which is current.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table_bucket_arn":{"type":"string","description":"Table bucket ARN."},
					"namespace":{"type":"string","description":"Namespace."},
					"name":{"type":"string","description":"Table name."}
				},
				"required":["table_bucket_arn","namespace","name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"metadata_location":{"type":"string"},
					"format_version":{"type":"integer"},
					"current_snapshot_id":{"type":"integer"},
					"snapshots":{"type":"array","items":{"type":"object"}}
				},
				"required":["snapshots"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution:   readOnly,
		},
	}
}

func (p *RuntimeProvider) dataLakeHandlers() map[string]mcp.HandlerFunc {
	return map[string]mcp.HandlerFunc{
		"runtime_athena_run_query":         p.toolAthenaRunQuery,
		"runtime_glue_describe_table":      p.toolGlueDescribeTable,
		"runtime_s3tables_table_snapshots": p.toolS3TablesTableSnapshots,
	}
}

// sdkConfig is an SDK configuration served in process by the router.
func (p *RuntimeProvider) sdkConfig() (aws.Config, error) {
	if p.router == nil {
		return aws.Config{}, fmt.Errorf("runtime router is unavailable")
	}
	return sdkconfig.InProcess(p.router, p.defaultRegion()), nil
}

func (p *RuntimeProvider) toolAthenaRunQuery(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		SQL            string `json:"sql"`
		WorkGroup      string `json:"workgroup"`
		Catalog        string `json:"catalog"`
		Database       string `json:"database"`
		OutputLocation string `json:"output_location"`
		MaxRows        int    `json:"max_rows"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.SQL) == "" {
		return nil, fmt.Errorf("sql is required")
	}
	cfg, err := p.sdkConfig()
	if err != nil {
		return nil, err
	}
	res, err := athenaquery.Run(ctx, athena.NewFromConfig(cfg), athenaquery.Request{
		SQL: args.SQL, WorkGroup: args.WorkGroup, Catalog: args.Catalog, Database: args.Database, OutputLocation: args.OutputLocation,
	}, athenaquery.Options{
		Timeout: boundedSeconds(args.TimeoutSeconds, athenaToolDefaultTimeout, athenaToolMaxTimeout),
		MaxRows: boundedInt(args.MaxRows, athenaToolDefaultRows, athenaToolMaxRows),
	})
	if err != nil {
		return nil, err
	}
	return athenaQueryOutput(res), nil
}

// athenaQueryOutput is a query's outcome in the tool's snake_case shape.
func athenaQueryOutput(res *athenaquery.Result) map[string]any {
	cols := make([]map[string]any, len(res.Columns))
	for i, c := range res.Columns {
		cols[i] = map[string]any{"name": c.Name, "type": c.Type}
	}
	rows := res.Rows
	if rows == nil {
		rows = [][]*string{}
	}
	out := map[string]any{
		"query_execution_id": res.QueryExecutionID,
		"state":              string(res.State),
		"statement_type":     string(res.StatementType),
		"columns":            cols,
		"rows":               rows,
		"truncated":          res.Truncated,
		"timed_out":          res.TimedOut,
	}
	if res.StateReason != "" {
		out["state_change_reason"] = res.StateReason
	}
	if res.Error != nil {
		out["error"] = map[string]any{
			"category": aws.ToInt32(res.Error.ErrorCategory), "type": aws.ToInt32(res.Error.ErrorType),
			"message": aws.ToString(res.Error.ErrorMessage), "retryable": res.Error.Retryable,
		}
	}
	if s := res.Statistics; s != nil {
		out["statistics"] = athenaStatistics(s)
	}
	if res.UpdateCount != nil {
		out["update_count"] = *res.UpdateCount
	}
	return out
}

func athenaStatistics(s *athenatypes.QueryExecutionStatistics) map[string]any {
	return map[string]any{
		"data_scanned_in_bytes":          aws.ToInt64(s.DataScannedInBytes),
		"engine_execution_time_millis":   aws.ToInt64(s.EngineExecutionTimeInMillis),
		"query_queue_time_millis":        aws.ToInt64(s.QueryQueueTimeInMillis),
		"total_execution_time_millis":    aws.ToInt64(s.TotalExecutionTimeInMillis),
		"service_processing_time_millis": aws.ToInt64(s.ServiceProcessingTimeInMillis),
	}
}

// boundedSeconds is n seconds, or def when n is unset, capped at limit.
func boundedSeconds(n int, def, limit time.Duration) time.Duration {
	if n <= 0 {
		return def
	}
	return min(time.Duration(n)*time.Second, limit)
}

// boundedInt is n, or def when n is unset, capped at limit.
func boundedInt(n, def, limit int) int {
	if n <= 0 {
		return def
	}
	return min(n, limit)
}
