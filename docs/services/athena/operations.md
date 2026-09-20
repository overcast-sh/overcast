---
title: "Athena operations"
description: "Every Athena operation Overcast declares — 12 of 12 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - athena
  - docs
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# Athena operations

All 12 listed operations are implemented. Back to [Athena](../athena.md).

## Summary

| Category   | ✅ Supported |
| ---------- | ------------ |
| Queries    | 5            |
| WorkGroups | 4            |
| Tags       | 3            |

---

## Endpoints

### Queries

| Operation             | Status       | Notes                                                                | AWS Docs                                                                                    |
| --------------------- | ------------ | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `StartQueryExecution` | ✅ Supported | Starts a query; immediately succeeds                                 | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_StartQueryExecution.html) |
| `GetQueryExecution`   | ✅ Supported | Returns query execution details                                      | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetQueryExecution.html)   |
| `GetQueryResults`     | ✅ Supported | Returns query results (empty result set)                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetQueryResults.html)     |
| `StopQueryExecution`  | ✅ Supported | Accepts a stop; queries are already SUCCEEDED, so state is unchanged | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_StopQueryExecution.html)  |
| `ListQueryExecutions` | ✅ Supported | Lists all query execution IDs                                        | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListQueryExecutions.html) |

### WorkGroups

| Operation         | Status       | Notes                     | AWS Docs                                                                                |
| ----------------- | ------------ | ------------------------- | --------------------------------------------------------------------------------------- |
| `CreateWorkGroup` | ✅ Supported | Creates a workgroup       | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_CreateWorkGroup.html) |
| `GetWorkGroup`    | ✅ Supported | Returns workgroup details | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetWorkGroup.html)    |
| `ListWorkGroups`  | ✅ Supported | Lists all workgroups      | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListWorkGroups.html)  |
| `DeleteWorkGroup` | ✅ Supported | Deletes a workgroup       | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_DeleteWorkGroup.html) |

### Tags

| Operation             | Status       | Notes                             | AWS Docs                                                                                    |
| --------------------- | ------------ | --------------------------------- | ------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported | Adds/updates tags on a workgroup  | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported | Removes tag keys from a workgroup | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported | Lists tags on a workgroup         | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListTagsForResource.html) |

## Related

- [Athena](../athena.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
