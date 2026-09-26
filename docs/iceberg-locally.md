---
title: "Iceberg locally"
description: "Build an Iceberg data stack against Overcast: an S3 Tables table written with PyIceberg or Spark, Iceberg tables in Glue queried with Athena, and a sample dataset to start from."
section: "Getting Started"
tags:
  - athena
  - docs
  - glue
  - iceberg
  - pyiceberg
  - s3tables
  - spark
---

# Iceberg locally

Create a table bucket, write to it with PyIceberg or Spark through the S3 Tables
Iceberg REST catalog, and query Iceberg tables in the Glue Data Catalog with
Athena. Every step runs the same on macOS, Linux and Windows.

```bash
overcast samples load analytics
overcast athena query "SELECT region, count(*) AS orders FROM sample_analytics.orders_parquet GROUP BY region" --output-location s3://overcast-sample-analytics/results/
```

The commands here run unchanged in PowerShell. Where a step differs by shell,
both forms are shown.

## Create an S3 Tables table

Save the table's schema as `orders.json`:

```json
{"iceberg": {"schema": {"fields": [
  {"name": "id", "type": "long", "required": true},
  {"name": "amount", "type": "decimal(10,2)"}
]}}}
```

Then create the bucket, a namespace and the table. In a POSIX shell:

```bash
ARN=$(overcast aws s3tables create-table-bucket --name analytics --query arn --output text)
overcast aws s3tables create-namespace --table-bucket-arn "$ARN" --namespace sales
overcast aws s3tables create-table --table-bucket-arn "$ARN" --namespace sales --name orders --format ICEBERG --metadata file://orders.json
```

In PowerShell:

```powershell
$ARN = overcast aws s3tables create-table-bucket --name analytics --query arn --output text
overcast aws s3tables create-namespace --table-bucket-arn $ARN --namespace sales
overcast aws s3tables create-table --table-bucket-arn $ARN --namespace sales --name orders --format ICEBERG --metadata file://orders.json
```

## Write with PyIceberg

Connect with the snippet the console's *Connect a client* panel gives for this
table. Set `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` to
anything first; `overcast env` prints them.

<!-- connect-snippet:pyiceberg -->
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
table = catalog.load_table("sales.orders")
print(table.scan().to_arrow())
```

Append rows, and each append is a snapshot:

```python
import pyarrow as pa
from decimal import Decimal

rows = pa.table({"id": [1, 2], "amount": [Decimal("9.99"), Decimal("20.00")]},
                schema=table.schema().as_arrow())
table.append(rows)
print([s.snapshot_id for s in table.snapshots()])
```

## Write with Spark

<!-- connect-snippet:spark -->
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
# then: spark.table("s3tables.sales.orders").show()
```

In the shell, `spark.sql("INSERT INTO s3tables.sales.orders VALUES (3, 5.00)")`
commits another snapshot. From PowerShell, run the same command on one line or
end each line with a backtick instead of a backslash.

## Query Iceberg with Athena

Athena runs Iceberg tables that live in the Glue Data Catalog on its engine.
`overcast samples load analytics` gives you one: once a query has started the
engine, loading the dataset again adds `orders_iceberg`, a copy of the orders
made with `CREATE TABLE AS`.

```bash
overcast athena query "SELECT count(*) FROM sample_analytics.orders_parquet" --output-location s3://overcast-sample-analytics/results/
overcast samples load analytics
overcast athena query "SELECT region, round(sum(quantity * unit_price), 2) AS revenue FROM sample_analytics.orders_iceberg GROUP BY region ORDER BY revenue DESC" --output-location s3://overcast-sample-analytics/results/
```

Your own Iceberg table is a `CREATE TABLE` with the Iceberg table type, then
`INSERT`, `MERGE`, `UPDATE` or `DELETE`:

```sql
CREATE TABLE sample_analytics.refunds (order_id bigint, amount double)
LOCATION 's3://overcast-sample-analytics/iceberg/refunds/'
TBLPROPERTIES ('table_type' = 'ICEBERG')
```

Athena lists the tables of an S3 Tables bucket under the
`s3tablescatalog/<bucket>` catalog, but cannot run a query in that catalog yet;
see [Athena's differences from AWS](./services/athena.md#differences-from-aws).

## Check each commit

- The console's S3 Tables page shows a table's snapshots and its metadata file
  by file.
- An agent connected to [Overcast's MCP endpoint](./configuration/mcp.md) calls
  `runtime_s3tables_table_snapshots` for the same list, and
  `runtime_athena_run_query` and `runtime_glue_describe_table` for the Athena
  and Glue side.
- `overcast aws s3tables get-table-metadata-location` names the current
  metadata file, which is in the table's warehouse bucket.

## Start over

The sample dataset, and anything else in these services, goes with one reset:

```bash
overcast reset athena glue s3 s3tables --yes
```

## Related

- [S3 Tables Iceberg REST catalog](./services/s3tables/iceberg-rest.md) — every endpoint, and Trino, DuckDB and AWS CLI snippets
- [Querying and sample data](./cli/data.md) — `overcast athena query` and `overcast samples load`
- [Athena](./services/athena.md)
- [Glue Data Catalog](./services/glue.md)
- [S3 Tables](./services/s3tables.md)
- [Apache Iceberg documentation](https://iceberg.apache.org/docs/latest/)
