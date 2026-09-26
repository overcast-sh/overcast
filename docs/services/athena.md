---
title: "Athena — Amazon Athena"
description: "Athena queries run for real on a Trino engine container, over tables in the Glue Data Catalog and data in S3, with results written to the output location."
section: "Service Reference"
tags:
  - amazon
  - athena
  - docs
  - services
  - sql
  - trino
---

# Athena — Amazon Athena

Athena queries run for real, on a Trino container Overcast starts on the first
query, over tables in the [Glue Data Catalog](./glue.md) and data in S3.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws s3 mb s3://results
aws athena start-query-execution --query-string 'SELECT 1 AS one' \
  --result-configuration OutputLocation=s3://results/
aws athena get-query-results --query-execution-id <id>
# once Status.State is SUCCEEDED: the header row "one", then "1"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

The first query waits, `QUEUED`, while the engine starts — 10–20 seconds with
the image already pulled, plus a 0.78 GB pull the first time ever. Later
queries start at once.

## What works

| Area | Behaviour |
| --- | --- |
| Queries | `SELECT`, `INSERT`, CTAS, `MERGE`, `UPDATE`, `DELETE`, views and every Trino function, run on the engine Athena engine version 3 is |
| Execution states | `QUEUED` while the engine starts, `RUNNING`, then `SUCCEEDED`, `FAILED` or `CANCELLED`; `StopQueryExecution` cancels the query on the engine |
| Failures | `AthenaError` with the error category and type Athena would give, and the engine's message as `StateChangeReason` |
| Results | `<id>.csv` (or `.txt` for DDL and utility statements) and its `.metadata` at `OutputLocation`; `GetQueryResults` pages, header row first for a `SELECT`, `UpdateCount` for DML |
| Statistics | `Statistics` and `GetQueryRuntimeStatistics` from the engine's own timings and bytes scanned |
| DDL | `CREATE EXTERNAL TABLE`, `CREATE DATABASE`, `DROP`, `ALTER TABLE ADD/DROP PARTITION`, `MSCK REPAIR TABLE`, `SHOW` and `DESCRIBE` write and read the Glue Data Catalog directly |
| Iceberg | `CREATE TABLE … TBLPROPERTIES ('table_type'='ICEBERG')`, then `INSERT`, `MERGE`, `UPDATE` and `DELETE` commit through Glue's `metadata_location` |
| S3 Tables | Each table bucket is the catalog `s3tablescatalog/<bucket>`, as the query's catalog or in a table's full name; see [S3 Tables](#s3-tables) |
| Formats | CSV, JSON, Parquet, ORC and Avro tables in S3, partitioned or not |
| Workgroups | Create, get, list, update and delete; the result location, its enforcement and `BytesScannedCutoffPerQuery` apply to each query |
| Idempotency | A repeated `ClientRequestToken` returns the same query or named query |
| Listing | `ListQueryExecutions` and `ListNamedQueries` cover one workgroup, `primary` by default, and paginate |
| Saved queries | Named queries and prepared statements, through the API or SQL `PREPARE` and `DEALLOCATE PREPARE`; `EXECUTE … USING` and `ExecutionParameters` bind their parameters |
| Data catalogs | `AwsDataCatalog` is built in; `GLUE`, `HIVE` and `LAMBDA` catalogs can be registered |
| Metadata | `GetDatabase`, `ListDatabases`, `GetTableMetadata` and `ListTableMetadata` read the Glue Data Catalog, including an S3 Tables bucket's `s3tablescatalog/<bucket>` catalog |
| Tags | On workgroup and data catalog ARNs |
| CloudFormation | `AWS::Athena::WorkGroup` (updated in place), `NamedQuery`, `PreparedStatement` and `DataCatalog` |

Without a Docker daemon, or with `ATHENA_ENGINE=inert`, the engine is off:
queries succeed at once with no rows, and DDL still reaches Glue. The
[configuration reference](../configuration/reference.md) lists the engine's
settings, and `/_overcast/athena/engine` reports its state.

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| SQL dialect | Athena engine version 3 | Trino 483, so dialect differences are rare; see [the list](./athena/limitations.md#dialect) |
| First query | Starts at once | Waits for the engine to start |
| Result reuse | `ResultReuseConfiguration` reuses a recent result | Every query runs |
| CloudWatch metrics | Published when the workgroup enables them | Not published |
| S3 Tables DDL | Iceberg DDL, except `ALTER TABLE RENAME`, `CREATE VIEW` and `ALTER DATABASE` | Also refuses a namespace's `COMMENT`, `LOCATION` or properties, and `SHOW PARTITIONS` |
| Data catalogs | `FEDERATED` provisions a connector | `FEDERATED` is refused with a 501 |
| Metadata | `LAMBDA` and `HIVE` catalogs are read through their connector | Only `GLUE` catalogs for this account are readable |
| Spark | Spark workgroups, sessions and notebooks | Not emulated |

The rest — result files, statistics, errors and the engine itself — is in
[Athena limitations](./athena/limitations.md).

## S3 Tables

A table bucket is queried as `s3tablescatalog/<bucket>`, as on AWS: set it as
the query's `Catalog` and a namespace as its `Database`, or name a table in
full, `"s3tablescatalog/<bucket>"."<namespace>"."<table>"`. A bucket needs no
registering, and one created while the engine runs is queryable at once.

- `SELECT`, `INSERT`, `MERGE`, `UPDATE` and `DELETE` run on the engine, and
  every write commits through S3 Tables' Iceberg REST catalog, moving the
  table's metadata location.
- `CREATE TABLE … TBLPROPERTIES ('table_type'='iceberg')` and CTAS create an
  S3 Tables table. Leave out `LOCATION` and `location`: S3 Tables chooses
  where a table lives. A CTAS is Iceberg and Parquet unless it says otherwise.
- `CREATE DATABASE` creates a namespace, and `DROP TABLE` and `DROP DATABASE`
  delete one. `SHOW` and `DESCRIBE` read the bucket.
- Hive's own table statements — `CREATE EXTERNAL TABLE`, partitions and
  `MSCK REPAIR TABLE` — fail `NOT_SUPPORTED`, since a table bucket holds only
  Iceberg tables.

With the engine off, `SHOW` and `DESCRIBE` still read a bucket, but a
statement that would change one fails `NOT_SUPPORTED` rather than succeed
having changed nothing.

## Gotchas

> [!NOTE]
> A query needs a result location, as on AWS: set one on the workgroup or
> pass `--result-configuration`. `primary` has none, so a bare
> `start-query-execution` against it fails with `InvalidRequestException`.

<!-- BEGIN overcast:capabilities -->

## Operations

All 37 listed operations are implemented.
Per-operation status, notes and AWS API links: [Athena operations](athena/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Athena limitations](./athena/limitations.md) — the engine, the dialect and the results in detail
- [Glue Data Catalog](./glue.md) — where Athena's table metadata lives
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/athena/latest/APIReference/)
