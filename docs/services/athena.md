---
title: "Athena — Amazon Athena"
description: "Athena's control plane — workgroups, queries, named queries, prepared statements, data catalogs and Glue metadata. Queries succeed without running, so results are empty."
section: "Service Reference"
tags:
  - amazon
  - athena
  - docs
  - services
---

# Athena — Amazon Athena

Athena's control plane is emulated: workgroups, saved queries, data catalogs
and the Glue metadata they read. No SQL runs, so every query succeeds at once
with an empty result set.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws athena create-work-group --name analytics
aws athena start-query-execution \
  --work-group analytics \
  --query-string 'SELECT 1' \
  --result-configuration OutputLocation=s3://results/

aws athena get-query-execution --query-execution-id <id>
# Status.State is already SUCCEEDED
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Workgroups | Create, get, list, update and delete. `primary` always exists and cannot be deleted |
| Workgroup settings | `UpdateWorkGroup` applies `ConfigurationUpdates`, including the `Remove*` flags |
| Result location | The workgroup's `ResultConfiguration` fills in what the query leaves out, and wins when `EnforceWorkGroupConfiguration` is set |
| Query executions | Full `QueryExecution`: context, statement type, engine version, parameters, statistics |
| Idempotency | A repeated `ClientRequestToken` returns the same query or named query |
| Listing | `ListQueryExecutions` and `ListNamedQueries` cover one workgroup, `primary` by default, and paginate |
| Saved queries | Named queries and prepared statements: create, get, batch get, list, update, delete |
| Data catalogs | `AwsDataCatalog` is built in; `GLUE`, `HIVE` and `LAMBDA` catalogs can be registered |
| Metadata | `GetDatabase`, `ListDatabases`, `GetTableMetadata` and `ListTableMetadata` read the [Glue Data Catalog](./glue.md) |
| Tags | On workgroup and data catalog ARNs |
| CloudFormation | `AWS::Athena::WorkGroup` (updated in place), `NamedQuery`, `PreparedStatement` and `DataCatalog` |

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| Query execution | The SQL runs | Nothing runs; the query is `SUCCEEDED` as soon as it starts |
| Results | Written to `OutputLocation` | Nothing is written, and `GetQueryResults` is empty |
| Execution states | `QUEUED` and `RUNNING` are observable | A query is never seen before it finishes |
| Statistics | Real timings and bytes scanned | Present, all zero |
| Prepared statements | `EXECUTE ... USING` runs them | Stored and returned, never run |
| Data catalogs | `FEDERATED` provisions a connector | `FEDERATED` is refused with a 501 |
| Metadata | `LAMBDA` and `HIVE` catalogs are read through their connector | Only `GLUE` catalogs for this account are readable |
| Spark | Spark workgroups, sessions and notebooks | Not emulated |

## Gotchas

> [!NOTE]
> A query needs a result location, as on AWS: set one on the workgroup or
> pass `--result-configuration`. `primary` has none, so a bare
> `start-query-execution` against it fails with `InvalidRequestException`.

Any assertion about the rows a query returns still needs real Athena.

<!-- BEGIN overcast:capabilities -->

## Operations

All 36 listed operations are implemented.
Per-operation status, notes and AWS API links: [Athena operations](athena/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Glue Data Catalog](./glue.md) — where Athena's table metadata lives
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/athena/latest/APIReference/)
