---
title: "S3 Tables — Amazon S3 Tables"
description: "S3 Tables — table buckets, namespaces and Iceberg tables, a real S3 warehouse bucket per table, and the Iceberg REST catalog. Maintenance and replication are stored but never run."
section: "Service Reference"
tags:
  - amazon
  - docs
  - iceberg
  - s3tables
  - services
---

# S3 Tables — Amazon S3 Tables

Table buckets, namespaces and Iceberg tables, each table backed by a real
warehouse bucket in Overcast's S3, with the Iceberg REST catalog that PyIceberg,
Spark and Trino connect to.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

ARN=$(aws s3tables create-table-bucket --name analytics --query arn --output text)
aws s3tables create-namespace --table-bucket-arn "$ARN" --namespace sales
aws s3tables create-table --table-bucket-arn "$ARN" --namespace sales \
  --name orders --format ICEBERG \
  --metadata '{"iceberg":{"schema":{"fields":[{"name":"id","type":"long","required":true}]}}}'

aws s3tables get-table-metadata-location --table-bucket-arn "$ARN" \
  --namespace sales --name orders
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Table buckets | Create, get, list (prefix and pagination), delete; AWS's naming rules; delete refuses a bucket that still has namespaces |
| Namespaces | Create, get, list, delete; delete refuses a namespace that still has tables |
| Tables | Create, get by ARN or by name, list, rename, delete; the ARN survives a rename |
| Warehouse | Every table gets its own `s3://…--table-s3` bucket in S3, which `CreateBucket` itself refuses to create |
| Initial metadata | `CreateTable` with `metadata.iceberg.schema` writes a format-version 2 `metadata.json` into the warehouse |
| Commits | `UpdateTableMetadataLocation` is a compare-and-swap on `versionToken`: a stale token is `ConflictException` |
| Iceberg REST catalog | Served at `/iceberg`: namespaces, tables, commits and staged create — see [the catalog page](./s3tables/iceberg-rest.md) |
| Configuration | Policies, encryption, storage class, metrics, maintenance, record expiration and replication are stored and read back |
| Tags | On table buckets and tables, on create or through the tagging operations |
| Glue and Athena | Every table bucket is a catalog under Glue's `s3tablescatalog`, which Athena queries and writes as `s3tablescatalog/<bucket>` — see [Glue](./glue.md#s3-tables-through-s3tablescatalog) and [Athena](./athena.md#s3-tables) |
| CloudFormation | `AWS::S3Tables::TableBucket`, `Namespace`, `Table`, `TableBucketPolicy` and `TablePolicy` |
| System map | A table bucket node lists its tables by namespace, with their warehouse folded in, and peeks at a table's latest commit |

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| Maintenance, expiration, replication | Jobs run on a schedule | Configuration is stored; job status reports that nothing has run |
| Iceberg REST staged create | `400 Bad Request` | Supported, so Trino can create tables |
| Deleting a table | Its data is removed asynchronously | The warehouse bucket and its objects stay in S3 |

The full list is on [S3 Tables limitations](./s3tables/limitations.md).

## Gotchas

> [!WARNING]
> Every S3 Tables path — `/buckets`, `/tables`, `/namespaces`, `/iceberg` and the
> rest — is also a legal S3 bucket name. Overcast tells them apart by the SigV4
> signing name, so a request must be signed for `s3tables` to reach S3 Tables. An
> unsigned request, or one signed for `s3`, reaches the S3 bucket of that name.
> Unsigned Iceberg clients use `/_overcast/s3tables/iceberg` instead.

<!-- BEGIN overcast:capabilities -->

## Operations

All 49 listed operations are implemented.
Per-operation status, notes and AWS API links: [S3 Tables operations](s3tables/operations.md).

<!-- END overcast:capabilities -->

## Related

- [S3 Tables limitations](./s3tables/limitations.md)
- [S3 Tables Iceberg REST catalog](./s3tables/iceberg-rest.md) — PyIceberg, Spark and the catalog's endpoints
- [S3](./s3.md) — where each table's warehouse bucket lives
- [Glue Data Catalog](./glue.md)
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_S3_Tables.html)
