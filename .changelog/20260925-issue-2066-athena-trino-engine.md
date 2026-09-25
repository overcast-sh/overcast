+ [athena] queries run for real on a Trino engine container, started on the first query, over Glue tables and data in S3.
  results land at `OutputLocation` as `<id>.csv` and `.csv.metadata`; `GetQueryResults` pages them, header row first.
  `AthenaError`, `Statistics`, `GetQueryRuntimeStatistics`, `StopQueryExecution` and `BytesScannedCutoffPerQuery` all apply.
+ [athena/glue] Athena's Hive DDL — `CREATE EXTERNAL TABLE`, partitions, `MSCK REPAIR TABLE`, `SHOW`, `DESCRIBE` — writes Glue.
  Iceberg `CREATE TABLE` and CTAS run on the engine, so `INSERT` and `MERGE` commit through Glue's `metadata_location`.
+ [glue] `UpdateColumnStatisticsForTable` and `…ForPartition`, and their `Get` and `Delete` counterparts.
+ [config] `ATHENA_ENGINE`, `ATHENA_ENGINE_IMAGE`, `ATHENA_ENGINE_MEMORY`, `ATHENA_DOCKER_SOCKET` and `ATHENA_KEEP_CONTAINERS`.
  `/_overcast/athena/engine` reports the engine's state and the timings of its last start.
~! [athena] with Docker available a query is `QUEUED`, then `RUNNING`, rather than `SUCCEEDED` as `StartQueryExecution` returns.
  the first query pulls a 2.4 GB image and waits for the engine to start.
  migration: poll `GetQueryExecution` until the query finishes, or set `ATHENA_ENGINE=inert` to keep queries inert.
