---
title: "S3 Tables operations"
description: "Every S3 Tables operation Overcast declares — 49 of 49 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - operations
  - s3tables
  - services
---

<!-- BEGIN overcast:capabilities -->

# S3 Tables operations

All 49 listed operations are implemented. Back to [S3 Tables](../s3tables.md).

## Summary

| Category                              | ✅ Supported | 🧊 Inert | ⚠️ Partial |
| ------------------------------------- | ------------ | -------- | ---------- |
| Table buckets                         | 4            |          |            |
| Namespaces                            | 4            |          |            |
| Tables                                | 6            |          | 1          |
| Policies                              | 4            | 2        |            |
| Encryption, storage class and metrics | 7            | 3        |            |
| Maintenance                           | 2            | 3        |            |
| Replication and record expiration     | 5            | 5        |            |
| Tags                                  | 3            |          |            |

---

## Endpoints

### Table buckets

| Operation           | Status       | Notes                                                                                                  | AWS Docs                                                                                          |
| ------------------- | ------------ | ------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------- |
| `CreateTableBucket` | ✅ Supported | Table bucket naming rules; `encryptionConfiguration`, `storageClassConfiguration` and `tags` on create | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_CreateTableBucket.html) |
| `GetTableBucket`    | ✅ Supported | Addressed by ARN; `type` is always `customer`                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucket.html)    |
| `ListTableBuckets`  | ✅ Supported | `prefix`, `type`, `maxBuckets` and `continuationToken`                                                 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_ListTableBuckets.html)  |
| `DeleteTableBucket` | ✅ Supported | `ConflictException` while the bucket still holds namespaces                                            | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTableBucket.html) |

### Namespaces

| Operation         | Status       | Notes                                                      | AWS Docs                                                                                        |
| ----------------- | ------------ | ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `CreateNamespace` | ✅ Supported | One-level namespaces, as AWS; namespace naming rules       | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_CreateNamespace.html) |
| `GetNamespace`    | ✅ Supported |                                                            | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetNamespace.html)    |
| `ListNamespaces`  | ✅ Supported | `prefix`, `maxNamespaces` and `continuationToken`          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_ListNamespaces.html)  |
| `DeleteNamespace` | ✅ Supported | `ConflictException` while the namespace still holds tables | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteNamespace.html) |

### Tables

| Operation                     | Status       | Notes                                                                                                                                                                                                                                                                                             | AWS Docs                                                                                                    |
| ----------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `CreateTable`                 | ⚠️ Partial   | Creates the table's `--table-s3` warehouse bucket in S3; `metadata.iceberg.schema` (primitive types) or `schemaV2` (struct, list and map included), `partitionSpec`, `writeOrder` and `properties` write the first `metadata.json`, with field ids reassigned depth-first as Iceberg assigns them | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_CreateTable.html)                 |
| `GetTable`                    | ✅ Supported | By `tableArn`, or by `tableBucketARN`, `namespace` and `name`; also served at its pre-2025-06 path binding, which older SDKs still send                                                                                                                                                           | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTable.html)                    |
| `ListTables`                  | ✅ Supported | `namespace`, `prefix`, `maxTables` and `continuationToken`                                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_ListTables.html)                  |
| `DeleteTable`                 | ✅ Supported | Optional `versionToken` check; the warehouse bucket and its objects are left in S3                                                                                                                                                                                                                | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTable.html)                 |
| `RenameTable`                 | ✅ Supported | New name and/or namespace; the ARN and version token are unchanged                                                                                                                                                                                                                                | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_RenameTable.html)                 |
| `GetTableMetadataLocation`    | ✅ Supported |                                                                                                                                                                                                                                                                                                   | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableMetadataLocation.html)    |
| `UpdateTableMetadataLocation` | ✅ Supported | Compare-and-swap on `versionToken`: a stale token is `ConflictException`; the location must be inside the warehouse                                                                                                                                                                               | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_UpdateTableMetadataLocation.html) |

### Policies

| Operation                 | Status       | Notes                              | AWS Docs                                                                                                |
| ------------------------- | ------------ | ---------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `PutTableBucketPolicy`    | 🧊 Inert     | Stored and returned; not evaluated | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableBucketPolicy.html)    |
| `GetTableBucketPolicy`    | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucketPolicy.html)    |
| `DeleteTableBucketPolicy` | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTableBucketPolicy.html) |
| `PutTablePolicy`          | 🧊 Inert     | Stored and returned; not evaluated | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTablePolicy.html)          |
| `GetTablePolicy`          | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTablePolicy.html)          |
| `DeleteTablePolicy`       | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTablePolicy.html)       |

### Encryption, storage class and metrics

| Operation                               | Status       | Notes                                                         | AWS Docs                                                                                                              |
| --------------------------------------- | ------------ | ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `PutTableBucketEncryption`              | 🧊 Inert     | Stored; new tables inherit it. Objects are not encrypted      | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableBucketEncryption.html)              |
| `GetTableBucketEncryption`              | ✅ Supported | `AES256` until configured otherwise                           | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucketEncryption.html)              |
| `DeleteTableBucketEncryption`           | ✅ Supported | Returns the bucket to `AES256`                                | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTableBucketEncryption.html)           |
| `GetTableEncryption`                    | ✅ Supported | The bucket's configuration at create time, or the table's own | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableEncryption.html)                    |
| `PutTableBucketStorageClass`            | 🧊 Inert     | Stored; new tables inherit it                                 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableBucketStorageClass.html)            |
| `GetTableBucketStorageClass`            | ✅ Supported | `STANDARD` until configured otherwise                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucketStorageClass.html)            |
| `GetTableStorageClass`                  | ✅ Supported |                                                               | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableStorageClass.html)                  |
| `PutTableBucketMetricsConfiguration`    | 🧊 Inert     | Recorded; no CloudWatch metrics are published                 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableBucketMetricsConfiguration.html)    |
| `GetTableBucketMetricsConfiguration`    | ✅ Supported | `NotFoundException` until configured                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucketMetricsConfiguration.html)    |
| `DeleteTableBucketMetricsConfiguration` | ✅ Supported |                                                               | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTableBucketMetricsConfiguration.html) |

### Maintenance

| Operation                                | Status       | Notes                                                          | AWS Docs                                                                                                               |
| ---------------------------------------- | ------------ | -------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `PutTableBucketMaintenanceConfiguration` | 🧊 Inert     | Stored and echoed; no maintenance runs                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableBucketMaintenanceConfiguration.html) |
| `GetTableBucketMaintenanceConfiguration` | ✅ Supported | AWS's defaults merged with what was put                        | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucketMaintenanceConfiguration.html) |
| `PutTableMaintenanceConfiguration`       | 🧊 Inert     | Stored and echoed; no compaction or snapshot management runs   | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableMaintenanceConfiguration.html)       |
| `GetTableMaintenanceConfiguration`       | ✅ Supported | AWS's defaults merged with what was put                        | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableMaintenanceConfiguration.html)       |
| `GetTableMaintenanceJobStatus`           | 🧊 Inert     | Every job reports `Not_Yet_Run`, or `Disabled` when turned off | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableMaintenanceJobStatus.html)           |

### Replication and record expiration

| Operation                               | Status       | Notes                                               | AWS Docs                                                                                                              |
| --------------------------------------- | ------------ | --------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `PutTableBucketReplication`             | 🧊 Inert     | Stored with a `versionToken`; nothing is replicated | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableBucketReplication.html)             |
| `GetTableBucketReplication`             | ✅ Supported |                                                     | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableBucketReplication.html)             |
| `DeleteTableBucketReplication`          | ✅ Supported | Optional `versionToken` check                       | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTableBucketReplication.html)          |
| `PutTableReplication`                   | 🧊 Inert     | Stored with a `versionToken`; nothing is replicated | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableReplication.html)                   |
| `GetTableReplication`                   | ✅ Supported |                                                     | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableReplication.html)                   |
| `DeleteTableReplication`                | ✅ Supported | `versionToken` check                                | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_DeleteTableReplication.html)                |
| `GetTableReplicationStatus`             | 🧊 Inert     | Every destination reports `pending`                 | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableReplicationStatus.html)             |
| `PutTableRecordExpirationConfiguration` | 🧊 Inert     | Stored and echoed; no records expire                | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_PutTableRecordExpirationConfiguration.html) |
| `GetTableRecordExpirationConfiguration` | ✅ Supported | `disabled` until configured                         | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableRecordExpirationConfiguration.html) |
| `GetTableRecordExpirationJobStatus`     | 🧊 Inert     | `NotYetRun` when enabled, otherwise `Disabled`      | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_GetTableRecordExpirationJobStatus.html)     |

### Tags

| Operation             | Status       | Notes                                                                    | AWS Docs                                                                                            |
| --------------------- | ------------ | ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported | Table buckets and tables; the 50-tag limit and AWS's key and value rules | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_TagResource.html)         |
| `UntagResource`       | ✅ Supported |                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported |                                                                          | [docs](https://docs.aws.amazon.com/AmazonS3/latest/API/API_s3TableBuckets_ListTagsForResource.html) |

---

## Emulator extensions

Operations Overcast serves that appear in no AWS model, so no SDK calls them and no AWS reference page describes them. They sit outside the operation counts above.

| Operation                      | Status       | Notes                                                                                                                                 |
| ------------------------------ | ------------ | ------------------------------------------------------------------------------------------------------------------------------------- |
| `IcebergGetConfig`             | ✅ Supported | Needs `warehouse`; the `prefix` override is the URL-encoded ARN, and the defaults point the client's FileIO at Overcast's S3          |
| `IcebergListNamespaces`        | ✅ Supported | `pageToken`/`pageSize`; a `parent` namespace has no children, since S3 Tables namespaces have one level                               |
| `IcebergCreateNamespace`       | ✅ Supported | One-level namespaces; any properties are kept, where AWS supports only `owner`                                                        |
| `IcebergLoadNamespaceMetadata` | ✅ Supported |                                                                                                                                       |
| `IcebergNamespaceExists`       | ✅ Supported | `HEAD`                                                                                                                                |
| `IcebergDropNamespace`         | ✅ Supported | `NamespaceNotEmptyException` while the namespace still holds tables                                                                   |
| `IcebergUpdateProperties`      | ✅ Supported | Not served by AWS; `UnprocessableEntityException` for a key both set and removed                                                      |
| `IcebergListTables`            | ✅ Supported | `pageToken`/`pageSize`                                                                                                                |
| `IcebergCreateTable`           | ✅ Supported | Writes the first metadata file; also accepts `stage-create`, which AWS refuses with 400                                               |
| `IcebergRegisterTable`         | ✅ Supported | Not served by AWS; the metadata's location must be an unused `--table-s3` warehouse                                                   |
| `IcebergLoadTable`             | ✅ Supported | Reads the table's current metadata file from its warehouse                                                                            |
| `IcebergTableExists`           | ✅ Supported | `HEAD`                                                                                                                                |
| `IcebergUpdateTable`           | ✅ Supported | Every format v1/v2 update and requirement, in `UpdateTableMetadataLocation`'s compare-and-swap; `CommitFailedException` on a conflict |
| `IcebergDropTable`             | ✅ Supported | Only with `purgeRequested=true`, as on AWS; the files stay in the warehouse                                                           |
| `IcebergRenameTable`           | ✅ Supported | Within the table bucket                                                                                                               |
| `IcebergReportMetrics`         | ✅ Supported | Not served by AWS; accepted and dropped                                                                                               |

## Related

- [S3 Tables](../s3tables.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
