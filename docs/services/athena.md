---
title: "Athena — Amazon Athena"
description: "Athena's control plane — workgroups, query executions and tags. Queries are recorded and reported SUCCEEDED without running, so every result set comes back empty."
section: "Service Reference"
tags:
  - amazon
  - athena
  - docs
  - services
---

# Athena — Amazon Athena

Athena's control plane is emulated; no SQL is executed, so every query succeeds
immediately with an empty result set.

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
| Workgroups | Create, get, list, delete; `Configuration` is stored and handed back verbatim |
| Query executions | `StartQueryExecution` records the SQL, workgroup and `OutputLocation` and returns an id |
| Polling | `GetQueryExecution` reports `SUCCEEDED`, with submission and completion timestamps |
| Results | `GetQueryResults` returns a well-formed but empty `ResultSet` |
| Stopping | `StopQueryExecution` accepts a known query id and rejects an unknown one |
| Tags | `TagResource`, `UntagResource` and `ListTagsForResource` on workgroup ARNs |

## Differences from AWS

| Area                    | On AWS                                                           | Overcast                                                                                                     |
| ----------------------- | ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| Query execution         | The SQL is parsed and run                                        | The string is stored, never parsed or run — a query over a Glue table returns nothing, not that table's rows |
| Results                 | Written to `OutputLocation`                                      | Nothing is written; the bucket stays empty                                                                   |
| Execution states        | `QUEUED`, `RUNNING`, `FAILED` and `CANCELLED`                    | None of them is ever observed                                                                                |
| Stopping a query        | `StopQueryExecution` interrupts a running query                  | Every query has already finished, so a stop is accepted and changes nothing                                  |
| Statistics              | `Statistics`, `EngineVersion` and data-scanned figures           | Absent                                                                                                       |
| Workgroup configuration | Result-location overrides and bytes-scanned cutoffs are enforced | Echoed, not enforced                                                                                         |

## Gotchas

> [!NOTE]
> A stack that provisions workgroups deploys, and code calling
> `StartQueryExecution` gets an id-shaped answer it can poll. Any assertion
> about the rows that come back needs real Athena.

<!-- BEGIN overcast:capabilities -->

## Operations

All 12 listed operations are implemented.
Per-operation status, notes and AWS API links: [Athena operations](athena/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Glue Data Catalog](./glue.md) — where Athena's table metadata lives
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/athena/latest/APIReference/)
