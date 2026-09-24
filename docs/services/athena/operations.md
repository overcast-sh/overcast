---
title: "Athena operations"
description: "Every Athena operation Overcast declares — 36 of 36 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - athena
  - docs
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# Athena operations

All 36 listed operations are implemented. Back to [Athena](../athena.md).

## Summary

| Category           | ✅ Supported | 🧊 Inert | ⚠️ Partial |
| ------------------ | ------------ | -------- | ---------- |
| Queries            | 4            | 2        |            |
| WorkGroups         | 6            |          |            |
| NamedQueries       | 6            |          |            |
| PreparedStatements | 6            |          |            |
| DataCatalogs       | 4            |          | 5          |
| Tags               | 3            |          |            |

---

## Endpoints

### Queries

| Operation                | Status       | Notes                                                                                                                                                     | AWS Docs                                                                                       |
| ------------------------ | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `StartQueryExecution`    | 🧊 Inert     | Resolves the result configuration against the workgroup and honours ClientRequestToken; no SQL runs, so the query succeeds at once having scanned nothing | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_StartQueryExecution.html)    |
| `GetQueryExecution`      | ✅ Supported | Full QueryExecution: context, statement type, engine version, resolved result configuration, statistics                                                   | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetQueryExecution.html)      |
| `BatchGetQueryExecution` | ✅ Supported | Unknown IDs come back as UnprocessedQueryExecutionIds                                                                                                     | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_BatchGetQueryExecution.html) |
| `ListQueryExecutions`    | ✅ Supported | One workgroup (primary by default), most recent first, paginated                                                                                          | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListQueryExecutions.html)    |
| `StopQueryExecution`     | ✅ Supported | Cancels an unfinished query; a finished one keeps its state                                                                                               | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_StopQueryExecution.html)     |
| `GetQueryResults`        | 🧊 Inert     | An empty result set for a SUCCEEDED query; an unfinished or failed query is InvalidRequestException                                                       | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetQueryResults.html)        |

### WorkGroups

| Operation            | Status       | Notes                                                                                                        | AWS Docs                                                                                   |
| -------------------- | ------------ | ------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------ |
| `CreateWorkGroup`    | ✅ Supported | Duplicate names are InvalidRequestException; engine version resolved                                         | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_CreateWorkGroup.html)    |
| `GetWorkGroup`       | ✅ Supported | Includes the built-in primary workgroup                                                                      | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetWorkGroup.html)       |
| `ListWorkGroups`     | ✅ Supported | Full summaries, paginated                                                                                    | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListWorkGroups.html)     |
| `UpdateWorkGroup`    | ✅ Supported | Description, State and ConfigurationUpdates, including the Remove* flags                                     | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_UpdateWorkGroup.html)    |
| `DeleteWorkGroup`    | ✅ Supported | primary cannot be deleted; a workgroup with named queries or prepared statements needs RecursiveDeleteOption | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_DeleteWorkGroup.html)    |
| `ListEngineVersions` | ✅ Supported | AUTO and Athena engine version 3                                                                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListEngineVersions.html) |

### NamedQueries

| Operation            | Status       | Notes                                         | AWS Docs                                                                                   |
| -------------------- | ------------ | --------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `CreateNamedQuery`   | ✅ Supported | Honours ClientRequestToken                    | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_CreateNamedQuery.html)   |
| `GetNamedQuery`      | ✅ Supported |                                               | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetNamedQuery.html)      |
| `BatchGetNamedQuery` | ✅ Supported |                                               | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_BatchGetNamedQuery.html) |
| `ListNamedQueries`   | ✅ Supported | One workgroup (primary by default), paginated | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListNamedQueries.html)   |
| `UpdateNamedQuery`   | ✅ Supported |                                               | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_UpdateNamedQuery.html)   |
| `DeleteNamedQuery`   | ✅ Supported |                                               | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_DeleteNamedQuery.html)   |

### PreparedStatements

| Operation                   | Status       | Notes                                       | AWS Docs                                                                                          |
| --------------------------- | ------------ | ------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `CreatePreparedStatement`   | ✅ Supported | Stored only; EXECUTE ... USING runs nothing | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_CreatePreparedStatement.html)   |
| `GetPreparedStatement`      | ✅ Supported |                                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetPreparedStatement.html)      |
| `BatchGetPreparedStatement` | ✅ Supported |                                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_BatchGetPreparedStatement.html) |
| `ListPreparedStatements`    | ✅ Supported |                                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListPreparedStatements.html)    |
| `UpdatePreparedStatement`   | ✅ Supported |                                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_UpdatePreparedStatement.html)   |
| `DeletePreparedStatement`   | ✅ Supported |                                             | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_DeletePreparedStatement.html)   |

### DataCatalogs

| Operation           | Status       | Notes                                                                  | AWS Docs                                                                                  |
| ------------------- | ------------ | ---------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `CreateDataCatalog` | ⚠️ Partial   | GLUE, LAMBDA and HIVE are registered; FEDERATED is not emulated        | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_CreateDataCatalog.html) |
| `GetDataCatalog`    | ✅ Supported | Includes the built-in AwsDataCatalog                                   | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetDataCatalog.html)    |
| `ListDataCatalogs`  | ✅ Supported |                                                                        | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListDataCatalogs.html)  |
| `UpdateDataCatalog` | ✅ Supported | AwsDataCatalog cannot be modified                                      | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_UpdateDataCatalog.html) |
| `DeleteDataCatalog` | ✅ Supported | AwsDataCatalog cannot be deleted                                       | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_DeleteDataCatalog.html) |
| `GetDatabase`       | ⚠️ Partial   | Reads the Glue Data Catalog; LAMBDA and HIVE catalogs are not readable | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetDatabase.html)       |
| `ListDatabases`     | ⚠️ Partial   | Reads the Glue Data Catalog; paginated                                 | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListDatabases.html)     |
| `GetTableMetadata`  | ⚠️ Partial   | Reads the Glue Data Catalog                                            | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_GetTableMetadata.html)  |
| `ListTableMetadata` | ⚠️ Partial   | Reads the Glue Data Catalog; Expression is a name regex; paginated     | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListTableMetadata.html) |

### Tags

| Operation             | Status       | Notes                        | AWS Docs                                                                                    |
| --------------------- | ------------ | ---------------------------- | ------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported | Workgroups and data catalogs | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported | Workgroups and data catalogs | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported | Workgroups and data catalogs | [docs](https://docs.aws.amazon.com/athena/latest/APIReference/API_ListTagsForResource.html) |

## Related

- [Athena](../athena.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
