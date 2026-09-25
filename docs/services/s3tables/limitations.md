---
title: "S3 Tables limitations"
description: "What S3 Tables stores without acting on, how its warehouse buckets behave, which Iceberg metadata Overcast writes, and which error messages are inferred."
section: "Service Reference"
tags:
  - docs
  - iceberg
  - limitations
  - s3tables
  - services
---

# S3 Tables limitations

The full divergence list behind [S3 Tables](../s3tables.md). The control plane
is complete; what is missing is everything AWS does in the background.

## Stored, never run

| Configuration | On AWS | Overcast |
| --- | --- | --- |
| Table maintenance (compaction, snapshot management) | Runs on the table's files | Stored and echoed. `GetTableMaintenanceJobStatus` reports `Not_Yet_Run`, or `Disabled` for a job turned off |
| Bucket maintenance (unreferenced file removal) | Deletes unreferenced files | Stored and echoed; reported the same way |
| Record expiration | Deletes expired records | Stored and echoed. `GetTableRecordExpirationJobStatus` reports `NotYetRun` when enabled, otherwise `Disabled` |
| Replication | Copies tables to destination buckets | Stored with a `versionToken`. `GetTableReplicationStatus` reports every destination `pending` |
| Metrics | Publishes CloudWatch request metrics | The configuration is recorded; nothing is published |
| Policies | Evaluated on every request | Stored and returned; never evaluated |
| Encryption and storage class | Applied to the table's objects | Stored, inherited by new tables, and returned; objects are not encrypted or tiered |

Maintenance reads answer with AWS's defaults for anything not configured:
compaction to 512 MB files, snapshot management keeping at least one snapshot
for 120 hours, and unreferenced file removal after 3 days (10 for non-current
files).

Record expiration is accepted on every table. AWS applies it to AWS-managed
tables such as S3 Metadata tables, and may refuse it on a table you created.

## Warehouse buckets

Each table's `warehouseLocation` is a bucket in Overcast's S3, named like AWS's
(`<uuid prefix>-<random>--table-s3`). It is an ordinary bucket once created:
`GetObject`, `PutObject` and `ListObjectsV2` work on it, and it appears in
`ListBuckets`, where AWS hides it.

`DeleteTable` removes the table from the catalog and leaves its warehouse
bucket, and every object in it, in S3. AWS deletes a table's data
asynchronously; Overcast does not delete it at all. Remove the bucket with the
S3 API if the space matters.

`CreateBucket` refuses a name ending in `--table-s3`, as S3 does, so only S3
Tables can create one.

## Iceberg metadata

`CreateTable` writes `metadata/00000-<uuid>.metadata.json` when
`metadata.iceberg.schema` is given, and sets `metadataLocation` to it. Without
a schema, `metadataLocation` is absent until a client commits one, as on AWS.

| Input | Handling |
| --- | --- |
| `schema` | Primitive Iceberg types only (`long`, `string`, `decimal(p,s)`, `fixed[n]`, …); column ids are reassigned 1..n |
| `partitionSpec` | Written as spec 0; partition field ids start at 1000 |
| `writeOrder` | Written as the default sort order |
| `properties` | Copied into the metadata's properties |
| `schemaV2` | Refused with `501 NotImplemented` |

Engines commit through the [Iceberg REST catalog](./iceberg-rest.md), which
writes format-version 1 and 2 metadata, nested types included; a table asking
for version 3 is refused. A client that writes its own metadata file into the
warehouse can commit it with `UpdateTableMetadataLocation` instead.

## Errors

Every client error's code and HTTP status come from the AWS model; the
exceptions are `schemaV2` (`501 NotImplemented`, above) and a failing state
store (`500 InternalError`). Some messages have not been observed from AWS and
are Overcast's wording:

| Condition | Code |
| --- | --- |
| `DeleteNamespace` on a namespace with tables | `ConflictException` |
| `DeleteTableBucket` on a bucket with namespaces | `ConflictException` |
| Reading a policy, metrics or replication configuration that does not exist | `NotFoundException` |
| A stale replication `versionToken` | `ConflictException` |

## Related

- [S3 Tables](../s3tables.md)
- [S3 Tables Iceberg REST catalog](./iceberg-rest.md)
- [S3](../s3.md)
- [All service pages](../README.md)
