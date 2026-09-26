---
title: "Glue — AWS Glue Data Catalog"
description: "Quick start, the Data Catalog operations that work — databases, tables, versions and partitions — how they differ from AWS, and what outside the catalog is not emulated."
section: "Service Reference"
tags:
  - aws
  - catalog
  - data
  - docs
  - glue
  - services
---

# Glue — AWS Glue Data Catalog

Only the Data Catalog is emulated: databases, tables, table versions and
partitions, kept as metadata exactly as you define them. ETL jobs, crawlers
and workflows are not.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws glue create-database --database-input Name=analytics
aws glue create-table --database-name analytics --table-input '{
  "Name": "events", "TableType": "EXTERNAL_TABLE",
  "PartitionKeys": [{"Name": "dt", "Type": "string"}],
  "StorageDescriptor": {"Location": "s3://data/events/",
    "Columns": [{"Name": "id", "Type": "bigint"}]}}'
aws glue create-partition --database-name analytics --table-name events \
  --partition-input 'Values=2024-01-01'
aws glue get-partitions --database-name analytics --table-name events \
  --expression "dt >= '2024-01-01'"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Databases | Create, get, list, update and delete. Deleting a database deletes its tables, partitions and table versions |
| Tables | Create, get, list, update, delete and batch delete, keeping the whole `TableInput` |
| Versions | `UpdateTable` archives the replaced definition unless `SkipArchive` is set |
| Concurrency | An `UpdateTable` whose `VersionId` is not current is `ConcurrentModificationException` |
| Partitions | Create, get, update and delete, singly and in batches |
| Partition queries | `GetPartitions` filters by `Expression`, pages, and honours `Segment` and `ExcludeColumnSchema` |
| Column statistics | Update, get and delete for a table or a partition, stored as written and deleted with their table or partition |
| Names | Database and table names are folded to lowercase, as on AWS |
| Errors | `AlreadyExistsException` on a duplicate, `EntityNotFoundException` for a missing parent, both HTTP 400 |
| Paging | `GetDatabases`, `GetTables`, `GetTableVersions` and `GetPartitions` take `MaxResults` and `NextToken` |
| Tags | `Tags` on `CreateDatabase`, and `TagResource`, `UntagResource` and `GetTags` on database and table ARNs |
| CloudFormation | `AWS::Glue::Database`, `AWS::Glue::Table` and `AWS::Glue::Partition` |

A stored table keeps everything `TableInput` carries — `StorageDescriptor`
with its columns, serde and `Location`, `PartitionKeys`, `Parameters` such as
`table_type` and `metadata_location`, view text — and gains `CreateTime`,
`UpdateTime` and `VersionId`.

### Partition expressions

A `GetPartitions` `Expression` may use `=`, `<>`, `!=`, `<`, `<=`, `>`, `>=`,
`AND`, `OR`, `NOT`, `IN`, `BETWEEN`, `LIKE`, `IS NULL` and parentheses over
the table's partition keys. Keys typed `int`, `bigint`, `long`, `smallint`,
`tinyint` or `decimal` compare as numbers; `string`, `char`, `varchar`, `date`
and `timestamp` keys compare as text. Anything else — a function call,
arithmetic, an unknown key — is `InvalidInputException` rather than a filter
that matches everything.

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| Jobs, crawlers, triggers, workflows, connections, schema registry, Data Quality | Full API | Not implemented; the Data Catalog only |
| `OpenTableFormatInput.IcebergInput` | Writes the table's initial Iceberg metadata | Sets `table_type=ICEBERG` and writes no metadata |
| `UpdateOpenTableFormatInput` | Applies Iceberg updates | 501 |
| Rename through an update | Not offered | A differing input `Name` is `InvalidInputException` |
| Cascading deletes | Asynchronous | Immediate |
| Partition indexes, column statistics, transactions | Supported | Ignored |
| `CatalogId` | Selects the catalog | Echoed, never used to separate catalogs |
| Lake Formation | Enforces grants | Reports the default `IAM_ALLOWED_PRINCIPALS` permission and enforces nothing |

An Iceberg table created through `IcebergInput` has no `metadata_location`,
and the response carries an `x-overcast-emulation-limitation` header saying
so.

## Gotchas

> [!NOTE]
> [Athena](./athena.md) does not query this catalogue yet. It records queries
> and returns empty result sets, so a table defined here changes nothing about
> what a query answers.

Iceberg clients that write their own metadata, such as PyIceberg's
`GlueCatalog`, commit through `UpdateTable` with `VersionId`. A lost race is
the same `ConcurrentModificationException` they retry on AWS.

## In the console

The Glue page lists databases, then each database's tables with their format,
location, partition keys and column count. A table opens on its schema, with
*Copy DDL* for the `CREATE EXTERNAL TABLE` that recreates it. The Partitions
tab sends a filter to `GetPartitions` as an `Expression`, exactly as typed, and
shows the service's parse error under the box. *Discover partitions* runs
`MSCK REPAIR TABLE` through Athena. The Data tab previews rows through Athena,
or lists the table's files when the query engine is off. The Versions tab diffs
any two table versions side by side.

*Create table from S3* stands in for a crawler, which Overcast does not
emulate. Pick a prefix, and the console reads the schema from one CSV, JSON
Lines or Parquet file, turns Hive `key=value/` folders into partition keys, and
creates the table with `CreateTable` and its partitions with
`BatchCreatePartition`. *Copy as CDK* gives the same table as a `CfnTable`.

<!-- BEGIN overcast:capabilities -->

## Operations

All 32 listed operations are implemented.
Per-operation status, notes and AWS API links: [Glue operations](glue/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Athena](./athena.md)
- [CDK resource type coverage](../cdk/resource-types.md)
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/glue/latest/webapi/)
