---
title: "Glue operations"
description: "Every Glue operation Overcast declares — 34 of 34 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - glue
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# Glue operations

All 34 listed operations are implemented. Back to [Glue](../glue.md).

## Summary

| Category          | ✅ Supported | ⚠️ Partial |
| ----------------- | ------------ | ---------- |
| Catalogs          | 1            | 1          |
| Databases         | 5            |            |
| Tables            | 4            | 2          |
| Table versions    | 4            |            |
| Partitions        | 7            | 1          |
| Column statistics | 6            |            |
| Tags              | 3            |            |

---

## Endpoints

### Catalogs

| Operation     | Status       | Notes                                                                           | AWS Docs                                                                         |
| ------------- | ------------ | ------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `GetCatalog`  | ✅ Supported | The account's catalog, s3tablescatalog and one child per S3 Tables table bucket | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetCatalog.html)  |
| `GetCatalogs` | ⚠️ Partial   | ParentCatalogId, IncludeRoot and Recursive; HasDatabases is not applied         | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetCatalogs.html) |

### Databases

| Operation        | Status       | Notes                                                                                         | AWS Docs                                                                            |
| ---------------- | ------------ | --------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `CreateDatabase` | ✅ Supported | Keeps the whole DatabaseInput and Tags; duplicate names are AlreadyExistsException            | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-CreateDatabase.html) |
| `GetDatabase`    | ✅ Supported | Returns the full database with CreateTime; an S3 Tables namespace in s3tablescatalog/<bucket> | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetDatabase.html)    |
| `GetDatabases`   | ✅ Supported | Paginated; s3tablescatalog/<bucket> lists the bucket's namespaces                             | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetDatabases.html)   |
| `UpdateDatabase` | ✅ Supported | Replaces the definition; renaming is refused                                                  | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-UpdateDatabase.html) |
| `DeleteDatabase` | ✅ Supported | Also deletes the database's tables, partitions and table versions                             | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-DeleteDatabase.html) |

### Tables

| Operation          | Status       | Notes                                                                                    | AWS Docs                                                                              |
| ------------------ | ------------ | ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| `CreateTable`      | ⚠️ Partial   | Keeps the whole TableInput; OpenTableFormatInput.IcebergInput writes no Iceberg metadata | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-CreateTable.html)      |
| `GetTable`         | ✅ Supported | Returns the full table; an S3 Tables table in s3tablescatalog/<bucket>                   | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetTable.html)         |
| `GetTables`        | ✅ Supported | Expression is a name regex; paginated; s3tablescatalog/<bucket> included                 | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetTables.html)        |
| `UpdateTable`      | ⚠️ Partial   | VersionId concurrency and archiving; UpdateOpenTableFormatInput is not implemented       | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-UpdateTable.html)      |
| `DeleteTable`      | ✅ Supported | Also deletes the table's partitions and versions                                         | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-DeleteTable.html)      |
| `BatchDeleteTable` | ✅ Supported | Reports missing tables in Errors                                                         | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-BatchDeleteTable.html) |

### Table versions

| Operation                 | Status       | Notes                                                            | AWS Docs                                                                                     |
| ------------------------- | ------------ | ---------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `GetTableVersion`         | ✅ Supported | Current version when VersionId is omitted                        | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetTableVersion.html)         |
| `GetTableVersions`        | ✅ Supported | Current and archived versions, newest first; paginated           | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetTableVersions.html)        |
| `DeleteTableVersion`      | ✅ Supported | Archived versions only; the current one is InvalidInputException | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-DeleteTableVersion.html)      |
| `BatchDeleteTableVersion` | ✅ Supported | Reports failed versions in Errors                                | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-BatchDeleteTableVersion.html) |

### Partitions

| Operation              | Status       | Notes                                                                                                 | AWS Docs                                                                                  |
| ---------------------- | ------------ | ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `CreatePartition`      | ✅ Supported | One value per partition key; duplicates are AlreadyExistsException                                    | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-CreatePartition.html)      |
| `BatchCreatePartition` | ✅ Supported | Up to 100; per-partition failures in Errors                                                           | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-BatchCreatePartition.html) |
| `GetPartition`         | ✅ Supported | Returns the full partition                                                                            | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetPartition.html)         |
| `GetPartitions`        | ⚠️ Partial   | Expression supports comparisons, AND/OR/NOT, IN, BETWEEN, LIKE, IS NULL; paginated, Segment supported | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetPartitions.html)        |
| `BatchGetPartition`    | ✅ Supported | Missing partitions are omitted                                                                        | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-BatchGetPartition.html)    |
| `UpdatePartition`      | ✅ Supported | Replaces the definition; new Values move the partition                                                | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-UpdatePartition.html)      |
| `DeletePartition`      | ✅ Supported | Deletes one partition                                                                                 | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-DeletePartition.html)      |
| `BatchDeletePartition` | ✅ Supported | Up to 25; missing partitions in Errors                                                                | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-BatchDeletePartition.html) |

### Column statistics

| Operation                            | Status       | Notes                                                     | AWS Docs                                                                                                |
| ------------------------------------ | ------------ | --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `UpdateColumnStatisticsForTable`     | ✅ Supported | Stored and echoed; an unknown column comes back in Errors | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-UpdateColumnStatisticsForTable.html)     |
| `UpdateColumnStatisticsForPartition` | ✅ Supported | Stored and echoed; an unknown column comes back in Errors | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-UpdateColumnStatisticsForPartition.html) |
| `GetColumnStatisticsForTable`        | ✅ Supported | A column with no statistics comes back in Errors          | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetColumnStatisticsForTable.html)        |
| `GetColumnStatisticsForPartition`    | ✅ Supported | A column with no statistics comes back in Errors          | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetColumnStatisticsForPartition.html)    |
| `DeleteColumnStatisticsForTable`     | ✅ Supported |                                                           | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-DeleteColumnStatisticsForTable.html)     |
| `DeleteColumnStatisticsForPartition` | ✅ Supported |                                                           | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-DeleteColumnStatisticsForPartition.html) |

### Tags

| Operation       | Status       | Notes                                           | AWS Docs                                                                           |
| --------------- | ------------ | ----------------------------------------------- | ---------------------------------------------------------------------------------- |
| `TagResource`   | ✅ Supported | Adds or overwrites tags on databases and tables | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-TagResource.html)   |
| `UntagResource` | ✅ Supported | Removes tags by key from databases and tables   | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-UntagResource.html) |
| `GetTags`       | ✅ Supported | Returns tags for databases and tables           | [docs](https://docs.aws.amazon.com/glue/latest/dg/aws-glue-api-GetTags.html)       |

## Related

- [Glue](../glue.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
