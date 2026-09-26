+ [cli/athena] `overcast athena query "<sql>"` runs a query, waits, and prints its rows as a table, JSON or CSV
  The engine's start-up progress goes to stderr, and a FAILED or CANCELLED query exits non-zero.
+ [cli/samples] `overcast samples load analytics` loads a sample dataset: partitioned CSV and Parquet in S3 with Glue tables over them
  With the Athena engine running it adds an Iceberg copy made with CTAS; the console's Load sample dataset action does the same load.
+ [mcp] Runtime MCP tools `runtime_athena_run_query`, `runtime_glue_describe_table` and `runtime_s3tables_table_snapshots`
+ [router/athena] `/_overcast/health`, `overcast status` and Metrics & Health report the Athena engine's state, memory and uptime
~ [cli] `overcast reset` takes several services, as in `overcast reset athena glue s3`
+ [docs] An *Iceberg locally* guide: S3 Tables from PyIceberg and Spark, and Iceberg tables in Glue queried with Athena
