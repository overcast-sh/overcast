---
title: "AppSync operations"
description: "Every AppSync operation Overcast declares — 81 of 81 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - appsync
  - docs
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# AppSync operations

All 81 listed operations are implemented. Back to [AppSync](../appsync.md).

## Summary

| Category                     | ✅ Supported |
| ---------------------------- | ------------ |
| GraphQL APIs                 | 5            |
| Schemas                      | 3            |
| API Keys                     | 4            |
| Data Sources                 | 5            |
| Functions                    | 5            |
| Resolvers                    | 6            |
| Tags                         | 3            |
| Environment Variables        | 2            |
| Domain Names                 | 5            |
| API Associations             | 3            |
| API Cache                    | 5            |
| Types                        | 5            |
| Merged APIs                  | 7            |
| Events API                   | 5            |
| Channel Namespaces           | 5            |
| Execution & Evaluation       | 2            |
| DynamoDB Resolver Operations | 11           |

---

## Endpoints

### GraphQL APIs

| Operation          | Status       | Notes | AWS Docs                                                                                  |
| ------------------ | ------------ | ----- | ----------------------------------------------------------------------------------------- |
| `CreateGraphqlApi` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateGraphqlApi.html) |
| `GetGraphqlApi`    | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetGraphqlApi.html)    |
| `ListGraphqlApis`  | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListGraphqlApis.html)  |
| `UpdateGraphqlApi` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateGraphqlApi.html) |
| `DeleteGraphqlApi` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteGraphqlApi.html) |

### Schemas

| Operation                 | Status       | Notes | AWS Docs                                                                                         |
| ------------------------- | ------------ | ----- | ------------------------------------------------------------------------------------------------ |
| `StartSchemaCreation`     | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_StartSchemaCreation.html)     |
| `GetSchemaCreationStatus` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetSchemaCreationStatus.html) |
| `GetIntrospectionSchema`  | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetIntrospectionSchema.html)  |

### API Keys

| Operation      | Status       | Notes | AWS Docs                                                                              |
| -------------- | ------------ | ----- | ------------------------------------------------------------------------------------- |
| `CreateApiKey` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateApiKey.html) |
| `ListApiKeys`  | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListApiKeys.html)  |
| `UpdateApiKey` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateApiKey.html) |
| `DeleteApiKey` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteApiKey.html) |

### Data Sources

| Operation          | Status       | Notes                                                                                                                                                                                                                  | AWS Docs                                                                                  |
| ------------------ | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `CreateDataSource` | ✅ Supported | AMAZON_DYNAMODB, AWS_LAMBDA, HTTP and NONE resolve at execution time; AMAZON_OPENSEARCH_SERVICE, AMAZON_ELASTICSEARCH, RELATIONAL_DATABASE, AMAZON_EVENTBRIDGE and AMAZON_BEDROCK_RUNTIME are accepted and stored only | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateDataSource.html) |
| `GetDataSource`    | ✅ Supported |                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetDataSource.html)    |
| `ListDataSources`  | ✅ Supported |                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListDataSources.html)  |
| `UpdateDataSource` | ✅ Supported |                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateDataSource.html) |
| `DeleteDataSource` | ✅ Supported |                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteDataSource.html) |

### Functions

| Operation        | Status       | Notes | AWS Docs                                                                                |
| ---------------- | ------------ | ----- | --------------------------------------------------------------------------------------- |
| `CreateFunction` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateFunction.html) |
| `GetFunction`    | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetFunction.html)    |
| `ListFunctions`  | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListFunctions.html)  |
| `UpdateFunction` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateFunction.html) |
| `DeleteFunction` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteFunction.html) |

### Resolvers

| Operation                 | Status       | Notes                                                                        | AWS Docs                                                                                         |
| ------------------------- | ------------ | ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `CreateResolver`          | ✅ Supported | UNIT and PIPELINE resolvers; requestMappingTemplate, responseMappingTemplate | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateResolver.html)          |
| `GetResolver`             | ✅ Supported |                                                                              | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetResolver.html)             |
| `ListResolvers`           | ✅ Supported |                                                                              | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListResolvers.html)           |
| `UpdateResolver`          | ✅ Supported |                                                                              | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateResolver.html)          |
| `DeleteResolver`          | ✅ Supported |                                                                              | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteResolver.html)          |
| `ListResolversByFunction` | ✅ Supported |                                                                              | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListResolversByFunction.html) |

### Tags

| Operation             | Status       | Notes | AWS Docs                                                                                     |
| --------------------- | ------------ | ----- | -------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListTagsForResource.html) |

### Environment Variables

| Operation                           | Status       | Notes | AWS Docs                                                                                                   |
| ----------------------------------- | ------------ | ----- | ---------------------------------------------------------------------------------------------------------- |
| `PutGraphqlApiEnvironmentVariables` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_PutGraphqlApiEnvironmentVariables.html) |
| `GetGraphqlApiEnvironmentVariables` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetGraphqlApiEnvironmentVariables.html) |

### Domain Names

| Operation          | Status       | Notes                             | AWS Docs                                                                                  |
| ------------------ | ------------ | --------------------------------- | ----------------------------------------------------------------------------------------- |
| `CreateDomainName` | ✅ Supported | Inert metadata; no routing effect | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateDomainName.html) |
| `GetDomainName`    | ✅ Supported |                                   | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetDomainName.html)    |
| `ListDomainNames`  | ✅ Supported |                                   | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListDomainNames.html)  |
| `UpdateDomainName` | ✅ Supported |                                   | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateDomainName.html) |
| `DeleteDomainName` | ✅ Supported |                                   | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteDomainName.html) |

### API Associations

| Operation           | Status       | Notes | AWS Docs                                                                                   |
| ------------------- | ------------ | ----- | ------------------------------------------------------------------------------------------ |
| `AssociateApi`      | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_AssociateApi.html)      |
| `GetApiAssociation` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetApiAssociation.html) |
| `DisassociateApi`   | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DisassociateApi.html)   |

### API Cache

| Operation        | Status       | Notes                                                                               | AWS Docs                                                                                |
| ---------------- | ------------ | ----------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `CreateApiCache` | ✅ Supported | Config stored; no actual caching enforced                                           | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateApiCache.html) |
| `GetApiCache`    | ✅ Supported |                                                                                     | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetApiCache.html)    |
| `UpdateApiCache` | ✅ Supported |                                                                                     | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateApiCache.html) |
| `DeleteApiCache` | ✅ Supported |                                                                                     | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteApiCache.html) |
| `FlushApiCache`  | ✅ Supported | No-op: there is no real cache to flush, matching CreateApiCache storing config only | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_FlushApiCache.html)  |

### Types

| Operation    | Status       | Notes | AWS Docs                                                                            |
| ------------ | ------------ | ----- | ----------------------------------------------------------------------------------- |
| `CreateType` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateType.html) |
| `GetType`    | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetType.html)    |
| `ListTypes`  | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListTypes.html)  |
| `UpdateType` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateType.html) |
| `DeleteType` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteType.html) |

### Merged APIs

| Operation                      | Status       | Notes                                                                                                                                                                                                                                                                                                                                                                      | AWS Docs                                                                                              |
| ------------------------------ | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `AssociateSourceGraphqlApi`    | ✅ Supported | Merges source API schema SDL into the merged API by textual concatenation (schema_parser.go's Merge); resolvers and data sources are not copied or proxied from the source API, so a merged field needs its own resolver/data source defined directly on the merged API to execute. Real AWS AUTO_MERGE additionally carries resolvers and data sources over automatically | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_AssociateSourceGraphqlApi.html)    |
| `AssociateMergedGraphqlApi`    | ✅ Supported | Same schema-only merge as AssociateSourceGraphqlApi                                                                                                                                                                                                                                                                                                                        | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_AssociateMergedGraphqlApi.html)    |
| `GetSourceApiAssociation`      | ✅ Supported |                                                                                                                                                                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetSourceApiAssociation.html)      |
| `ListSourceApiAssociations`    | ✅ Supported |                                                                                                                                                                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListSourceApiAssociations.html)    |
| `DisassociateSourceGraphqlApi` | ✅ Supported |                                                                                                                                                                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DisassociateSourceGraphqlApi.html) |
| `DisassociateMergedGraphqlApi` | ✅ Supported |                                                                                                                                                                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DisassociateMergedGraphqlApi.html) |
| `StartSchemaMerge`             | ✅ Supported | Re-runs the same schema-only merge as AssociateSourceGraphqlApi                                                                                                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_StartSchemaMerge.html)             |

### Events API

| Operation   | Status       | Notes                              | AWS Docs                                                                           |
| ----------- | ------------ | ---------------------------------- | ---------------------------------------------------------------------------------- |
| `CreateApi` | ✅ Supported | GRAPHQL and MERGED event API types | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateApi.html) |
| `GetApi`    | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetApi.html)    |
| `ListApis`  | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListApis.html)  |
| `UpdateApi` | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateApi.html) |
| `DeleteApi` | ✅ Supported |                                    | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteApi.html) |

### Channel Namespaces

| Operation                | Status       | Notes | AWS Docs                                                                                        |
| ------------------------ | ------------ | ----- | ----------------------------------------------------------------------------------------------- |
| `CreateChannelNamespace` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateChannelNamespace.html) |
| `GetChannelNamespace`    | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetChannelNamespace.html)    |
| `ListChannelNamespaces`  | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListChannelNamespaces.html)  |
| `UpdateChannelNamespace` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateChannelNamespace.html) |
| `DeleteChannelNamespace` | ✅ Supported |       | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteChannelNamespace.html) |

### Execution & Evaluation

| Operation                 | Status       | Notes                                                                 | AWS Docs                                                                                         |
| ------------------------- | ------------ | --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `EvaluateMappingTemplate` | ✅ Supported | Evaluates VTL mapping templates; logs and outErrors are not populated | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_EvaluateMappingTemplate.html) |
| `EvaluateCode`            | ✅ Supported | Evaluates APPSYNC_JS resolver code; outErrors is not populated        | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_EvaluateCode.html)            |

### DynamoDB Resolver Operations

| Operation            | Status       | Notes                                   | AWS Docs                                                                                    |
| -------------------- | ------------ | --------------------------------------- | ------------------------------------------------------------------------------------------- |
| `GetItem`            | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_GetItem.html)            |
| `PutItem`            | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_PutItem.html)            |
| `DeleteItem`         | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_DeleteItem.html)         |
| `UpdateItem`         | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_UpdateItem.html)         |
| `Query`              | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_Query.html)              |
| `Scan`               | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_Scan.html)               |
| `BatchGetItem`       | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_BatchGetItem.html)       |
| `BatchWriteItem`     | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_BatchWriteItem.html)     |
| `TransactGetItems`   | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_TransactGetItems.html)   |
| `TransactWriteItems` | ✅ Supported | DynamoDB data source resolver operation | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_TransactWriteItems.html) |
| `ConditionCheck`     | ✅ Supported | DynamoDB transact-write condition check | [docs](https://docs.aws.amazon.com/appsync/latest/APIReference/API_ConditionCheck.html)     |

---

## Emulator extensions

Operations Overcast serves that appear in no AWS model, so no SDK calls them and no AWS reference page describes them. They sit outside the operation counts above.

| Operation        | Status       | Notes                                                                                                                                                                         |
| ---------------- | ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ExecuteGraphQL` | ✅ Supported | Executes a GraphQL operation against the API; Cognito bearer tokens are decoded only, or verified against the local user pool when OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH=true |

## Related

- [AppSync](../appsync.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
