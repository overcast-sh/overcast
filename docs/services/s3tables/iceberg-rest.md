---
title: "S3 Tables Iceberg REST catalog"
description: "Connect PyIceberg, Spark and other Iceberg clients to S3 Tables through the Iceberg REST catalog at /iceberg: configuration, endpoints, and where it differs from AWS."
section: "Service Reference"
tags:
  - docs
  - iceberg
  - pyiceberg
  - s3tables
  - services
  - spark
---

# S3 Tables Iceberg REST catalog

The Iceberg REST catalog that [S3 Tables](../s3tables.md) serves at `/iceberg`,
as AWS serves it at `https://s3tables.<region>.amazonaws.com/iceberg`. Point a
client at Overcast's endpoint instead and configure it the way the AWS docs do.

## PyIceberg

```python
from pyiceberg.catalog import load_catalog

catalog = load_catalog("s3tables", **{
    "type": "rest",
    "uri": "http://localhost:4566/iceberg",
    "warehouse": "arn:aws:s3tables:us-east-1:000000000000:bucket/analytics",
    "rest.sigv4-enabled": "true",
    "rest.signing-name": "s3tables",
    "rest.signing-region": "us-east-1",
})
catalog.create_namespace("sales")
table = catalog.create_table("sales.orders", schema=schema)
table.append(rows)
print(table.scan().to_arrow())
```

Set `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` to anything:
Overcast checks the signing name, not the signature. There is no S3 endpoint to
configure, because the catalog's config response supplies Overcast's.

## Spark

```bash
spark-shell \
  --packages org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.9.2,org.apache.iceberg:iceberg-aws-bundle:1.9.2 \
  --conf spark.sql.extensions=org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions \
  --conf spark.sql.catalog.s3tables=org.apache.iceberg.spark.SparkCatalog \
  --conf spark.sql.catalog.s3tables.type=rest \
  --conf spark.sql.catalog.s3tables.uri=http://localhost:4566/iceberg \
  --conf spark.sql.catalog.s3tables.warehouse=arn:aws:s3tables:us-east-1:000000000000:bucket/analytics \
  --conf spark.sql.catalog.s3tables.rest.sigv4-enabled=true \
  --conf spark.sql.catalog.s3tables.rest.signing-name=s3tables \
  --conf spark.sql.catalog.s3tables.rest.signing-region=us-east-1 \
  --conf spark.sql.catalog.s3tables.io-impl=org.apache.iceberg.aws.s3.S3FileIO
```

## Clients without SigV4

`/iceberg` is also a legal S3 bucket name, so Overcast sends a request there to
the catalog only when it is signed for `s3tables`. A client that cannot sign
uses the same catalog at `http://localhost:4566/_overcast/s3tables/iceberg`,
with no credentials.

## Endpoints

Every path after `/v1/config` starts with `/v1/{prefix}`, where the prefix is
the URL-encoded table bucket ARN that `/v1/config?warehouse=<ARN>` returns.

| Endpoint | Notes |
| --- | --- |
| `GET /v1/config` | Needs `warehouse`; lists the endpoints below |
| Namespaces: list, create, load, `HEAD`, drop | One level, as in S3 Tables; drop refuses a namespace with tables |
| Tables: list, create, load, `HEAD`, rename | Create writes `metadata/00000-<uuid>.metadata.json` into the table's warehouse |
| `DELETE …/tables/{table}` | Only with `purgeRequested=true`, as on AWS |
| `POST …/tables/{table}` (commit) | All table updates and requirements of format versions 1 and 2 |
| `POST …/namespaces/{namespace}/properties` | Overcast only; see below |
| `POST …/namespaces/{namespace}/register` | Overcast only; see below |
| `POST …/tables/{table}/metrics` | Overcast only; accepted and dropped |

Views, multi-table transactions, scan planning and credential vending answer
`501 UnsupportedOperationException`.

A commit writes the next metadata file and moves the table's pointer in the
same compare-and-swap as `UpdateTableMetadataLocation`, so a REST client and an
S3 Tables API client can commit to the same table. A commit made against a
stale table gets `409 CommitFailedException`, and the new version token means a
stale `UpdateTableMetadataLocation` gets `ConflictException`.

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| Staged create (`stage-create`) | `400 Bad Request`, so CTAS cannot run | Supported; the commit that creates the table must carry `assert-create` |
| Namespace properties | Only `owner` | Any property, and `updateProperties` |
| Register, metrics reports | Not served | Served |
| `/v1/config` defaults | None | `s3.endpoint`, `s3.path-style-access` and the region, pointing the client's FileIO at Overcast |
| Unsigned access | Every request is signed | Also served at `/_overcast/s3tables/iceberg` |
| Dropping a table | Its files are removed in the background | Its files stay in the warehouse |

Staged create is supported because Trino creates tables through it, `CREATE
TABLE` and CTAS alike, and cannot write to S3 Tables without it. A client that
works here and must also work on AWS should not rely on it, nor on the
Overcast-only endpoints.

A registered table's metadata must name a whole `--table-s3` warehouse bucket
that no other table uses, and the metadata file must lie under it. The table
takes its ARN's id from the metadata's `table-uuid`.

The config defaults point the client's FileIO at Overcast's S3; anything the
client sets itself wins.

A staged create makes the table's warehouse bucket straight away, because the
engine writes data files there before it commits. If the commit never comes,
the bucket stays.

A table created with `CreateTable` but no schema has no metadata, so loading it
through the catalog answers `NoSuchTableException` until something commits to
it.

## Related

- [S3 Tables](../s3tables.md)
- [S3 Tables operations](./operations.md)
- [S3 Tables limitations](./limitations.md)
- [Using AWS SDKs and CLI](../../sdk-cli.md)
- [The S3 Tables Iceberg REST endpoint on AWS](https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-tables-integrating-open-source.html)
- [Iceberg REST catalog spec](https://github.com/apache/iceberg/blob/main/open-api/rest-catalog-open-api.yaml)
- [PyIceberg catalog configuration](https://py.iceberg.apache.org/configuration/)
