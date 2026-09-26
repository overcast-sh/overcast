---
title: "Querying and sample data"
description: "overcast athena query runs one Athena query and prints its rows as a table, JSON or CSV; overcast samples load analytics loads a sample dataset into S3, Glue and Athena."
section: "Reference"
tags:
  - athena
  - cli
  - docs
  - glue
  - overcast
  - samples
---

# Querying and sample data

`overcast athena query` runs one query and prints the result; `overcast samples
load analytics` gives it something to query.

```bash
overcast samples load analytics
overcast athena query "SELECT * FROM sample_analytics.orders_csv LIMIT 5" --output-location s3://overcast-sample-analytics/results/
```

Part of the [CLI reference](../cli.md). Both reach the daemon the way
[`overcast aws`](./aws.md#overcast-aws-args) does: `--endpoint`, else
`OVERCAST_ENDPOINT` or `OVERCAST_PORT`, in `OVERCAST_REGION` (default
`us-east-1`).

## `overcast athena query <sql>`

Starts the query, waits for it, and prints its rows. The first query after the
daemon starts waits for the query engine, and prints each start-up step to
stderr. A query that ends `FAILED` or `CANCELLED` prints Athena's reason and
exits with status 1, which makes the command a quick smoke test of a stack.

| Flag | Default | Description |
| --- | --- | --- |
| `--database` | — | Database the query runs in. |
| `--workgroup` | `primary` | Workgroup to run in. |
| `--catalog` | `AwsDataCatalog` | Data catalog. |
| `--output-location` | the workgroup's | `s3://` location for the results; needed when the workgroup has none. |
| `--output`, `-o` | `table` | `table`, `json` (one object per row) or `csv`. |

```bash
overcast athena query "SHOW TABLES" --database sample_analytics --output-location s3://overcast-sample-analytics/results/
overcast athena query "SELECT product, quantity FROM sample_analytics.orders_parquet LIMIT 3" -o json --output-location s3://overcast-sample-analytics/results/ | jq '.[0]'
```

A NULL prints as `NULL` in a table, `null` in JSON and an empty field in CSV.
Pass `-` as the SQL to read it from stdin, which keeps a long query out of the
shell's quoting. In a POSIX shell:

```bash
overcast athena query - --database sample_analytics -o csv --output-location s3://overcast-sample-analytics/results/ <<'SQL'
SELECT region, round(sum(quantity * unit_price), 2) AS revenue
FROM orders_parquet
GROUP BY region
SQL
```

In PowerShell:

```powershell
@'
SELECT region, round(sum(quantity * unit_price), 2) AS revenue
FROM orders_parquet
GROUP BY region
'@ | overcast athena query - --database sample_analytics -o csv --output-location s3://overcast-sample-analytics/results/
```


## `overcast samples load <dataset>`

Loads a sample dataset through the AWS API: the same load as the console's
*Load sample dataset* action. The one dataset is `analytics`, a month of 300
web-shop orders:

| Resource | What it holds |
| --- | --- |
| Bucket `overcast-sample-analytics` | The orders as CSV, one file per region under `region=<name>/`, and as one Parquet file; `results/` for query results |
| `sample_analytics.orders_csv` | A Glue table over the CSV, partitioned by `region` |
| `sample_analytics.orders_parquet` | A Glue table over the Parquet |
| `sample_analytics.orders_iceberg` | An Iceberg copy, made with `CREATE TABLE AS`, only while the Athena engine is running |

The data is the same on every load and on every machine, and loading again
changes nothing, except to add the Iceberg copy once the engine has started.
It prints each step to stderr and the tables to stdout:

```bash
overcast samples load analytics
```

`overcast reset athena glue s3` removes it, with everything else in those
three services.

## Related

- [Iceberg locally](../iceberg-locally.md) — PyIceberg, Spark and Athena on the same data
- [Athena](../services/athena.md) — what the engine runs, and how it differs from AWS
- [Wiping and importing state](./state.md) — `overcast reset`
