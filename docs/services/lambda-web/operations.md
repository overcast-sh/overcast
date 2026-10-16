---
title: "Lambda Web Functions operations"
description: "Every Lambda Web Functions operation Overcast declares — 20 of 20 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - lambda
  - operations
  - services
  - web
---

<!-- BEGIN overcast:capabilities -->

# Lambda Web Functions operations

All 20 listed operations are implemented. Back to [Lambda Web Functions](../lambda-web.md).

## Summary

| Category        | ✅ Supported | ⚠️ Partial |
| --------------- | ------------ | ---------- |
| Web functions   | 4            |            |
| Revisions       | 3            | 1          |
| Endpoints       | 3            | 2          |
| Resource policy | 2            | 1          |
| Tags            | 3            |            |
| Account         | 1            |            |

---

## Endpoints

### Web functions

| Operation           | Status       | Notes                                                                                                   | AWS Docs |
| ------------------- | ------------ | ------------------------------------------------------------------------------------------------------- | -------- |
| `CreateWebFunction` | ✅ Supported | Optional first revision and endpoint; tags; the create response reports `Pending`, later reads `Active` |          |
| `GetWebFunction`    | ✅ Supported | By name or ARN                                                                                          |          |
| `ListWebFunctions`  | ✅ Supported | `state` filter; `maxResults` and `nextToken` pagination                                                 |          |
| `DeleteWebFunction` | ✅ Supported | Deletes the function's revisions, endpoints, tags and policy with it                                    |          |

### Revisions

| Operation                   | Status       | Notes                                                                                                                                                                      | AWS Docs |
| --------------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| `CreateWebFunctionRevision` | ⚠️ Partial   | Validated and stored; the S3 package is not fetched and no code runs. `LatestRevision` endpoints are pointed at it. Past 50 revisions the oldest unrouted ones are deleted |          |
| `GetWebFunctionRevision`    | ✅ Supported | By revision ID or ARN; `timeoutSeconds` and `maxConcurrencyPerEnvironment` defaults are returned                                                                           |          |
| `ListWebFunctionRevisions`  | ✅ Supported | `state` filter; oldest first                                                                                                                                               |          |
| `DeleteWebFunctionRevision` | ✅ Supported | `ConflictException` while an endpoint routes traffic to the revision                                                                                                       |          |

### Endpoints

| Operation                   | Status       | Notes                                                                                                                                                                                         | AWS Docs |
| --------------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| `CreateWebFunctionEndpoint` | ⚠️ Partial   | `HomeRegion`, `MultiRegion` and `PerRegion`; revision weights, `autoDeploymentMode`, scaling and throttle config; at most 10 per function. The domain name is allocated but nothing serves it |          |
| `GetWebFunctionEndpoint`    | ✅ Supported | By name or ARN; `regionalEndpoints` has an entry per Region                                                                                                                                   |          |
| `ListWebFunctionEndpoints`  | ✅ Supported | `authType`, `autoDeploymentMode`, `endpointType`, `state` and `updateStatus` filters                                                                                                          |          |
| `UpdateWebFunctionEndpoint` | ⚠️ Partial   | Omitted members are kept and an empty `scalingConfig` or `throttleConfig` clears; takes effect at once, and no traffic flows to observe it                                                    |          |
| `DeleteWebFunctionEndpoint` | ✅ Supported | Removed at once; the `Deleting` state is not observable                                                                                                                                       |          |

### Resource policy

| Operation              | Status       | Notes                                                                                                                                                    | AWS Docs |
| ---------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| `PutResourcePolicy`    | ⚠️ Partial   | Public policies are rejected with `ValidationException`; `RevisionId` is checked. The policy is stored, but no endpoint request is authorised against it |          |
| `GetResourcePolicy`    | ✅ Supported | `ResourceNotFoundException` when none is attached                                                                                                        |          |
| `DeleteResourcePolicy` | ✅ Supported | `RevisionId` is checked when given                                                                                                                       |          |

### Tags

| Operation       | Status       | Notes                                                         | AWS Docs |
| --------------- | ------------ | ------------------------------------------------------------- | -------- |
| `TagResource`   | ✅ Supported | The web function's ARN only; a tag that exists is overwritten |          |
| `UntagResource` | ✅ Supported |                                                               |          |
| `ListTags`      | ✅ Supported |                                                               |          |

### Account

| Operation               | Status       | Notes                                                                          | AWS Docs |
| ----------------------- | ------------ | ------------------------------------------------------------------------------ | -------- |
| `GetWebAccountSettings` | ✅ Supported | AWS's documented default quotas; `functionCount` is the Region's web functions |          |

## Related

- [Lambda Web Functions](../lambda-web.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
