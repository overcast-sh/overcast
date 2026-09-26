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
| Image | `ghcr.io/overcast-sh/overcast-athena-engine:483`, Trino 483 with only the Hive and Iceberg connectors, pinned by digest: 0.78 GB to pull, 1.86 GB on disk | `ATHENA_ENGINE_IMAGE` |
| Memory limit | 1 GiB, of which half is the engine's heap | `ATHENA_ENGINE_MEMORY` |
| Docker endpoint | Lambda's | `ATHENA_DOCKER_SOCKET` |
| Keep stopped containers | Off | `ATHENA_KEEP_CONTAINERS` |

The engine loads every connector its image carries, and the default image
carries only Hive and Iceberg. A stock `trinodb/trino` image set through
`ATHENA_ENGINE_IMAGE` works too, but loads all of its connectors, not just
those two: it starts slower and uses more memory, and may need a larger
`ATHENA_ENGINE_MEMORY`. The engine is tuned for one small query at a time. A query that
needs more memory than the limit allows fails with an
`EXCEEDED_*_MEMORY_LIMIT` error; raise `ATHENA_ENGINE_MEMORY`.

Measured over three runs of the engine's tests with the image already pulled,
on Docker Desktop 29.8 on Windows 11 with 24 cores and other containers
running. Memory is as Docker reports it. Expect the start to be slower on a
laptop.

| Measure | Default image |
| --- | --- |
| Pull, linux/amd64, compressed | 0.78 GB |
| On disk (containerd image store) | 1.86 GB |
| First query, including the engine's start | 14.7–19.1 s |
| Engine start | 12.7–16.8 s |
| Memory after the first query | 542–588 MiB |
| Memory after a CTAS and an Iceberg `MERGE` | 646–705 MiB |

`GET /_overcast/athena/engine` reports the engine's state — `off`, `probing`,
`stopped`, `pulling`, `starting`, `ready` or `failed` — with the last start's
timings and error. `probing` lasts from Overcast starting until it knows
whether Docker is there; a query started then waits, `QUEUED`, rather than
running inert.

The value `ATHENA_ENGINE_MEMORY` takes is a Docker memory size such as `2g`
or `1536m`, at least `1g`.

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
| `DESCRIBE t column`, `DESCRIBE t PARTITION (…)` | Describe one column or partition | Not parsed |
| `ExecutionParameters` | Each one a single literal | Joined into `USING` as written, so each must be one SQL literal |

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
| Result size | Written in full | Up to 1 GiB of values; raise it with `ATHENA_MAX_RESULT_BYTES` |

A result over the limit fails its query with `ErrorCategory` 2 and
`ErrorType` 0, and nothing of it is kept. The limit takes a size such as
`4g`. The result `GetQueryResults` reads lives with the rest of Overcast's
state, in memory unless state is persisted.

## Credentials

The engine calls Glue and S3 in Overcast through a listener of its own, on an
address a container can reach: natively Overcast's API listens on loopback
only. The listener is plain HTTP and serves only Glue's JSON operations and
S3, and only to requests signed with an access key minted when Overcast
starts, which only the engine's configuration carries. When no narrower
address can be proved reachable it binds every interface, and Overcast logs a
warning.

The engine signs with Overcast's default secret key, so
`OVERCAST_SIGV4_VALIDATE` accepts its requests. With `OVERCAST_ENFORCE_IAM`
on, they are evaluated like any other, and a key with no policies is denied,
so queries that read tables fail.

## Related

- [Athena](../athena.md)
- [Glue Data Catalog](../glue.md)
- [Environment variable reference](../../configuration/reference.md)
- [Athena engine version 3](https://docs.aws.amazon.com/athena/latest/ug/engine-versions-reference-0003.html)
