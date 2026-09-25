---
title: "Athena limitations"
description: "How Athena's queries run in Overcast: the Trino engine and its cost, where its dialect differs from Athena's, and what differs in results, statistics and errors."
section: "Service Reference"
tags:
  - athena
  - docs
  - limitations
  - services
  - trino
---

# Athena limitations

The full divergence list behind [Athena](../athena.md): the engine queries run
on, the few places its SQL differs from Athena's, and what differs around a
query.

## The engine

Queries run on one Trino container per Overcast, started on the first query
that needs it and stopped after 15 minutes with nothing to run. The next query
starts it again.

| Setting | Default | Change it with |
| --- | --- | --- |
| Engine | `trino`; `inert` runs no SQL | `ATHENA_ENGINE` |
| Image | `trinodb/trino:483`, pinned by digest, 2.4 GB on disk | `ATHENA_ENGINE_IMAGE` |
| Memory limit | 1 GiB, of which half is the engine's heap | `ATHENA_ENGINE_MEMORY` |
| Docker endpoint | Lambda's | `ATHENA_DOCKER_SOCKET` |
| Keep stopped containers | Off | `ATHENA_KEEP_CONTAINERS` |

Only the Hive and Iceberg connectors are loaded, and the engine is tuned for
one small query at a time. A query that needs more memory than the limit
allows fails with an `EXCEEDED_*_MEMORY_LIMIT` error; raise
`ATHENA_ENGINE_MEMORY`.

Measured with the image already pulled, on Docker Desktop 29.7 on Windows 11
with 24 cores and other containers running: the first query took 11.6 s, of
which 9.7 s was the engine starting, and the container used 547 MiB after it
and 658 MiB after a CTAS and an Iceberg `MERGE`, as Docker reports memory.
Expect the start to be slower on a laptop.

`GET /_overcast/athena/engine` reports the engine's state — `off`, `stopped`,
`pulling`, `starting`, `ready` or `failed` — with the last start's timings and
error.

## Dialect

Athena engine version 3 is Trino, so a query that runs on Athena runs here.
The differences are in Hive DDL, which Overcast parses itself, and in the few
statements Athena added to Trino.

| Area | On AWS | Overcast |
| --- | --- | --- |
| OpenCSVSerDe columns | Read as their declared types | Read as `varchar`; declare them `string` and `CAST` |
| `ALTER TABLE` other than `ADD`/`DROP PARTITION` | Hive DDL (`ADD COLUMNS`, `RENAME TO`, `SET TBLPROPERTIES`) | Sent to Trino, which accepts only its own `ALTER TABLE` syntax |
| `SHOW CREATE TABLE` | Hive DDL | Trino's `CREATE TABLE … WITH (…)` |
| `DESCRIBE` | Hive's padded columns | The same columns, padded the same way, without `EXTENDED` detail |
| `OPTIMIZE` and `VACUUM` | Athena's Iceberg maintenance statements | Not parsed; use Trino's `ALTER TABLE … EXECUTE optimize` and `expire_snapshots` |
| `UNLOAD` | Writes a query's result to S3 | Not supported; use CTAS |
| CTAS `write_compression`, `vacuum_*` | Applied | Ignored |
| User-defined functions | `USING EXTERNAL FUNCTION` calls a Lambda | Not supported |
| Error messages | Name `awsdatacatalog` | An Iceberg table's error may name `awsdatacatalog_iceberg` |

## Around the query

| Area | On AWS | Overcast |
| --- | --- | --- |
| `.metadata` result file | Athena's binary encoding | The result's `ResultSetMetadata`, as JSON |
| `DataScannedInBytes` | Bytes Athena bills for | Bytes the engine read from S3 |
| `GetQueryRuntimeStatistics` | `Timeline`, `Rows` and the `OutputStage` plan tree | `Timeline` and `Rows` |
| Bytes-scanned cutoff | Cancels the query when it passes the cutoff | The same, checked as the engine reports progress, so it can read a little past it |
| Managed query results | Kept in Athena-owned storage | Readable through `GetQueryResults` only |
| Queries across a restart | Carry on | A query still running when Overcast stops, or left running by a crash, is `FAILED` |
| Other GLUE catalogs | Read their account's catalog | Every query reads this account's `AwsDataCatalog` |

A large result is held in memory while it is written, so a query returning
millions of rows costs Overcast that much memory.

## Credentials

The engine calls Glue and S3 in Overcast as access key `overcast-athena`,
through a listener of its own that it can always reach. With
`OVERCAST_ENFORCE_IAM` on, those calls are evaluated like any other, and a key
with no policies is denied, so queries that read tables fail.

## Related

- [Athena](../athena.md)
- [Glue Data Catalog](../glue.md)
- [Environment variable reference](../../configuration/reference.md)
- [Athena engine version 3](https://docs.aws.amazon.com/athena/latest/ug/engine-versions-reference-0003.html)
