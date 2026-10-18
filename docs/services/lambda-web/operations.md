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

| Category        | ✅ Supported |
| --------------- | ------------ |
| Web functions   | 4            |
| Revisions       | 4            |
| Endpoints       | 5            |
| Resource policy | 3            |
| Tags            | 3            |
| Account         | 1            |

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

| Operation                   | Status       | Notes                                                                                                                                                                                                                                                                                                                                                      | AWS Docs |
| --------------------------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| `CreateWebFunctionRevision` | ✅ Supported | Validated, pinned to an S3 object version and created `Pending`; a worker checks the package and the revision becomes `Active` or `Failed`. Docker runs the code on the first request. `LatestRevision` endpoints are pointed at it once `Active`. Reserved environment variable names are refused. Past 50 revisions the oldest unrouted ones are deleted |          |
| `GetWebFunctionRevision`    | ✅ Supported | By revision ID or ARN; `timeoutSeconds` and `maxConcurrencyPerEnvironment` defaults are returned                                                                                                                                                                                                                                                           |          |
| `ListWebFunctionRevisions`  | ✅ Supported | `state` filter; newest first                                                                                                                                                                                                                                                                                                                               |          |
| `DeleteWebFunctionRevision` | ✅ Supported | `ConflictException` while an endpoint routes traffic to the revision                                                                                                                                                                                                                                                                                       |          |

### Endpoints

| Operation                   | Status       | Notes                                                                                                                                                                                                                                                               | AWS Docs |
| --------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| `CreateWebFunctionEndpoint` | ✅ Supported | `HomeRegion`, `MultiRegion` and `PerRegion`; revision weights, `autoDeploymentMode`, scaling and throttle config; at most 10 per function. The domain name serves HTTP on the caller's host; other Regions of a `MultiRegion` endpoint are served from this process |          |
| `GetWebFunctionEndpoint`    | ✅ Supported | By name or ARN; `regionalEndpoints` has an entry per Region                                                                                                                                                                                                         |          |
| `ListWebFunctionEndpoints`  | ✅ Supported | `authType`, `autoDeploymentMode`, `endpointType`, `state` and `updateStatus` filters                                                                                                                                                                                |          |
| `UpdateWebFunctionEndpoint` | ✅ Supported | Omitted members are kept and an empty `scalingConfig` or `throttleConfig` clears; `updateStatus` goes `InProgress` then `Successful`. Revision weights must name `Active` revisions; the new routing is live for the next request                                   |          |
| `DeleteWebFunctionEndpoint` | ✅ Supported | Goes `Deleting` while the data plane drains, then is removed                                                                                                                                                                                                        |          |

### Resource policy

| Operation              | Status       | Notes                                                                                                                                                                                                                     | AWS Docs |
| ---------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| `PutResourcePolicy`    | ✅ Supported | Public policies are rejected with `ValidationException`; `RevisionId` is checked. `lambda:InvokeWebFunctionEndpoint` on an `IamAuth` endpoint is evaluated against it, for same- and cross-account callers and CloudFront |          |
| `GetResourcePolicy`    | ✅ Supported | `ResourceNotFoundException` when none is attached                                                                                                                                                                         |          |
| `DeleteResourcePolicy` | ✅ Supported | `RevisionId` is checked when given                                                                                                                                                                                        |          |

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
