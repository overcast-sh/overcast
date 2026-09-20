---
title: "CloudFront operations"
description: "Every CloudFront operation Overcast declares — 88 of 88 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - cloudfront
  - docs
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# CloudFront operations

All 88 listed operations are implemented. Back to [CloudFront](../cloudfront.md).

## Summary

| Category      | ✅ Supported |
| ------------- | ------------ |
| Distributions | 7            |
| Invalidations | 3            |
| OAC / OAI     | 11           |
| Tagging       | 3            |
| Policies      | 18           |
| Functions     | 8            |
| Keys & Crypto | 12           |
| Monitoring    | 8            |
| FLE           | 12           |
| Deployment    | 6            |

---

## Endpoints

### Distributions

| Operation                    | Status       | Notes                                                           | AWS Docs                                                                                               |
| ---------------------------- | ------------ | --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `CreateDistribution`         | ✅ Supported | CallerReference idempotency; Status always "Deployed"           | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateDistribution.html)         |
| `GetDistribution`            | ✅ Supported | Returns ETag header                                             | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetDistribution.html)            |
| `GetDistributionConfig`      | ✅ Supported | Returns DistributionConfig portion + ETag                       | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetDistributionConfig.html)      |
| `UpdateDistribution`         | ✅ Supported | Requires If-Match ETag; bumps version                           | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateDistribution.html)         |
| `DeleteDistribution`         | ✅ Supported | Requires If-Match + Enabled=false                               | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteDistribution.html)         |
| `ListDistributions`          | ✅ Supported | Marker/MaxItems pagination via serviceutil.Paginate             | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListDistributions.html)          |
| `CreateDistributionWithTags` | ✅ Supported | Creates distribution + tags atomically; _custom_id_ tag support | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateDistributionWithTags.html) |

### Invalidations

| Operation            | Status       | Notes                                                                    | AWS Docs                                                                                       |
| -------------------- | ------------ | ------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `CreateInvalidation` | ✅ Supported | Supports path and tag invalidations (#tag); Status instantly "Completed" | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateInvalidation.html) |
| `GetInvalidation`    | ✅ Supported | Returns invalidation by distribution + invalidation ID                   | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetInvalidation.html)    |
| `ListInvalidations`  | ✅ Supported | Marker/MaxItems pagination                                               | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListInvalidations.html)  |

### OAC / OAI

| Operation                                 | Status       | Notes                                                 | AWS Docs                                                                                                            |
| ----------------------------------------- | ------------ | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `CreateOriginAccessControl`               | ✅ Supported | Generates ID, returns ETag                            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateOriginAccessControl.html)               |
| `GetOriginAccessControl`                  | ✅ Supported | Returns OAC by ID with ETag                           | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetOriginAccessControl.html)                  |
| `UpdateOriginAccessControl`               | ✅ Supported | Requires If-Match ETag; bumps version                 | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateOriginAccessControl.html)               |
| `DeleteOriginAccessControl`               | ✅ Supported | Requires If-Match ETag                                | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteOriginAccessControl.html)               |
| `ListOriginAccessControls`                | ✅ Supported | Marker/MaxItems pagination                            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListOriginAccessControls.html)                |
| `CreateCloudFrontOriginAccessIdentity`    | ✅ Supported | CallerReference required; generates S3CanonicalUserId | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateCloudFrontOriginAccessIdentity.html)    |
| `GetCloudFrontOriginAccessIdentity`       | ✅ Supported | Returns OAI by ID with ETag                           | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetCloudFrontOriginAccessIdentity.html)       |
| `GetCloudFrontOriginAccessIdentityConfig` | ✅ Supported | Returns config portion + ETag                         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetCloudFrontOriginAccessIdentityConfig.html) |
| `UpdateCloudFrontOriginAccessIdentity`    | ✅ Supported | Requires If-Match ETag; bumps version                 | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateCloudFrontOriginAccessIdentity.html)    |
| `DeleteCloudFrontOriginAccessIdentity`    | ✅ Supported | Requires If-Match ETag                                | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteCloudFrontOriginAccessIdentity.html)    |
| `ListCloudFrontOriginAccessIdentities`    | ✅ Supported | Marker/MaxItems pagination                            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListCloudFrontOriginAccessIdentities.html)    |

### Tagging

| Operation             | Status       | Notes                         | AWS Docs                                                                                        |
| --------------------- | ------------ | ----------------------------- | ----------------------------------------------------------------------------------------------- |
| `ListTagsForResource` | ✅ Supported | Returns tags by resource ARN  | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListTagsForResource.html) |
| `TagResource`         | ✅ Supported | Merges tags into existing set | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported | Removes specified tag keys    | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UntagResource.html)       |

### Policies

| Operation                        | Status       | Notes                                 | AWS Docs                                                                                                   |
| -------------------------------- | ------------ | ------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `CreateCachePolicy`              | ✅ Supported | Generates ID, returns ETag            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateCachePolicy.html)              |
| `GetCachePolicy`                 | ✅ Supported | Returns policy by ID with ETag        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetCachePolicy.html)                 |
| `GetCachePolicyConfig`           | ✅ Supported | Returns config portion + ETag         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetCachePolicyConfig.html)           |
| `UpdateCachePolicy`              | ✅ Supported | Requires If-Match ETag; bumps version | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateCachePolicy.html)              |
| `DeleteCachePolicy`              | ✅ Supported | Requires If-Match ETag                | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteCachePolicy.html)              |
| `ListCachePolicies`              | ✅ Supported | Marker/MaxItems pagination            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListCachePolicies.html)              |
| `CreateOriginRequestPolicy`      | ✅ Supported | Generates ID, returns ETag            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateOriginRequestPolicy.html)      |
| `GetOriginRequestPolicy`         | ✅ Supported | Returns policy by ID with ETag        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetOriginRequestPolicy.html)         |
| `GetOriginRequestPolicyConfig`   | ✅ Supported | Returns config portion + ETag         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetOriginRequestPolicyConfig.html)   |
| `UpdateOriginRequestPolicy`      | ✅ Supported | Requires If-Match ETag; bumps version | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateOriginRequestPolicy.html)      |
| `DeleteOriginRequestPolicy`      | ✅ Supported | Requires If-Match ETag                | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteOriginRequestPolicy.html)      |
| `ListOriginRequestPolicies`      | ✅ Supported | Marker/MaxItems pagination            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListOriginRequestPolicies.html)      |
| `CreateResponseHeadersPolicy`    | ✅ Supported | Generates ID, returns ETag            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateResponseHeadersPolicy.html)    |
| `GetResponseHeadersPolicy`       | ✅ Supported | Returns policy by ID with ETag        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetResponseHeadersPolicy.html)       |
| `GetResponseHeadersPolicyConfig` | ✅ Supported | Returns config portion + ETag         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetResponseHeadersPolicyConfig.html) |
| `UpdateResponseHeadersPolicy`    | ✅ Supported | Requires If-Match ETag; bumps version | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateResponseHeadersPolicy.html)    |
| `DeleteResponseHeadersPolicy`    | ✅ Supported | Requires If-Match ETag                | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteResponseHeadersPolicy.html)    |
| `ListResponseHeadersPolicies`    | ✅ Supported | Marker/MaxItems pagination            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListResponseHeadersPolicies.html)    |

### Functions

| Operation          | Status       | Notes                                                                                                                                         | AWS Docs                                                                                     |
| ------------------ | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `CreateFunction`   | ✅ Supported | Stores code + config; Stage=DEVELOPMENT; returns ETag                                                                                         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateFunction.html)   |
| `DescribeFunction` | ✅ Supported | Returns FunctionSummary with metadata                                                                                                         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DescribeFunction.html) |
| `GetFunction`      | ✅ Supported | Returns raw function code (base64) with ETag                                                                                                  | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetFunction.html)      |
| `UpdateFunction`   | ✅ Supported | Requires If-Match ETag; bumps version                                                                                                         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateFunction.html)   |
| `DeleteFunction`   | ✅ Supported | Requires If-Match ETag                                                                                                                        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteFunction.html)   |
| `ListFunctions`    | ✅ Supported | Filters by Stage query param; MaxItems pagination                                                                                             | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListFunctions.html)    |
| `TestFunction`     | ✅ Supported | Returns a fixed success result without running the code; a function attached to a behaviour does execute on requests through the distribution | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_TestFunction.html)     |
| `PublishFunction`  | ✅ Supported | Promotes DEVELOPMENT → LIVE stage                                                                                                             | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_PublishFunction.html)  |

### Keys & Crypto

| Operation            | Status       | Notes                                      | AWS Docs                                                                                       |
| -------------------- | ------------ | ------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `CreateKeyGroup`     | ✅ Supported | Generates ID, returns ETag                 | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateKeyGroup.html)     |
| `GetKeyGroup`        | ✅ Supported | Returns key group by ID with ETag          | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetKeyGroup.html)        |
| `GetKeyGroupConfig`  | ✅ Supported | Returns config portion + ETag              | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetKeyGroupConfig.html)  |
| `UpdateKeyGroup`     | ✅ Supported | Requires If-Match ETag; bumps version      | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateKeyGroup.html)     |
| `DeleteKeyGroup`     | ✅ Supported | Requires If-Match ETag                     | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteKeyGroup.html)     |
| `ListKeyGroups`      | ✅ Supported | MaxItems pagination                        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListKeyGroups.html)      |
| `CreatePublicKey`    | ✅ Supported | CallerReference dedup; generates ID + ETag | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreatePublicKey.html)    |
| `GetPublicKey`       | ✅ Supported | Returns public key by ID with ETag         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetPublicKey.html)       |
| `GetPublicKeyConfig` | ✅ Supported | Returns config portion + ETag              | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetPublicKeyConfig.html) |
| `UpdatePublicKey`    | ✅ Supported | Requires If-Match ETag; bumps version      | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdatePublicKey.html)    |
| `DeletePublicKey`    | ✅ Supported | Requires If-Match ETag                     | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeletePublicKey.html)    |
| `ListPublicKeys`     | ✅ Supported | MaxItems pagination                        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListPublicKeys.html)     |

### Monitoring

| Operation                      | Status       | Notes                                            | AWS Docs                                                                                                 |
| ------------------------------ | ------------ | ------------------------------------------------ | -------------------------------------------------------------------------------------------------------- |
| `CreateMonitoringSubscription` | ✅ Supported | Per-distribution; requires existing distribution | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateMonitoringSubscription.html) |
| `GetMonitoringSubscription`    | ✅ Supported | Returns subscription by distribution ID          | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetMonitoringSubscription.html)    |
| `DeleteMonitoringSubscription` | ✅ Supported | Removes subscription for distribution            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteMonitoringSubscription.html) |
| `CreateRealtimeLogConfig`      | ✅ Supported | Name-based; generates ARN; duplicate name check  | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateRealtimeLogConfig.html)      |
| `GetRealtimeLogConfig`         | ✅ Supported | Lookup by Name or ARN in request body            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetRealtimeLogConfig.html)         |
| `UpdateRealtimeLogConfig`      | ✅ Supported | Updates by Name in request body                  | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateRealtimeLogConfig.html)      |
| `DeleteRealtimeLogConfig`      | ✅ Supported | Deletes by Name or ARN in request body           | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteRealtimeLogConfig.html)      |
| `ListRealtimeLogConfigs`       | ✅ Supported | MaxItems pagination                              | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListRealtimeLogConfigs.html)       |

### FLE

| Operation                              | Status       | Notes                                  | AWS Docs                                                                                                         |
| -------------------------------------- | ------------ | -------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `CreateFieldLevelEncryptionConfig`     | ✅ Supported | CallerReference required; generates ID | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateFieldLevelEncryptionConfig.html)     |
| `GetFieldLevelEncryption`              | ✅ Supported | Returns FLE config by ID with ETag     | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetFieldLevelEncryption.html)              |
| `GetFieldLevelEncryptionConfig`        | ✅ Supported | Returns config portion + ETag          | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetFieldLevelEncryptionConfig.html)        |
| `UpdateFieldLevelEncryptionConfig`     | ✅ Supported | Requires If-Match ETag; bumps version  | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateFieldLevelEncryptionConfig.html)     |
| `DeleteFieldLevelEncryption`           | ✅ Supported | Requires If-Match ETag                 | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteFieldLevelEncryption.html)           |
| `ListFieldLevelEncryptionConfigs`      | ✅ Supported | MaxItems pagination                    | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListFieldLevelEncryptionConfigs.html)      |
| `CreateFieldLevelEncryptionProfile`    | ✅ Supported | CallerReference required; generates ID | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateFieldLevelEncryptionProfile.html)    |
| `GetFieldLevelEncryptionProfile`       | ✅ Supported | Returns FLE profile by ID with ETag    | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetFieldLevelEncryptionProfile.html)       |
| `GetFieldLevelEncryptionProfileConfig` | ✅ Supported | Returns config portion + ETag          | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetFieldLevelEncryptionProfileConfig.html) |
| `UpdateFieldLevelEncryptionProfile`    | ✅ Supported | Requires If-Match ETag; bumps version  | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateFieldLevelEncryptionProfile.html)    |
| `DeleteFieldLevelEncryptionProfile`    | ✅ Supported | Requires If-Match ETag                 | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteFieldLevelEncryptionProfile.html)    |
| `ListFieldLevelEncryptionProfiles`     | ✅ Supported | MaxItems pagination                    | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListFieldLevelEncryptionProfiles.html)     |

### Deployment

| Operation                             | Status       | Notes                                 | AWS Docs                                                                                                        |
| ------------------------------------- | ------------ | ------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `CreateContinuousDeploymentPolicy`    | ✅ Supported | Generates ID, returns ETag            | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_CreateContinuousDeploymentPolicy.html)    |
| `GetContinuousDeploymentPolicy`       | ✅ Supported | Returns policy by ID with ETag        | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetContinuousDeploymentPolicy.html)       |
| `GetContinuousDeploymentPolicyConfig` | ✅ Supported | Returns config portion + ETag         | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_GetContinuousDeploymentPolicyConfig.html) |
| `UpdateContinuousDeploymentPolicy`    | ✅ Supported | Requires If-Match ETag; bumps version | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_UpdateContinuousDeploymentPolicy.html)    |
| `DeleteContinuousDeploymentPolicy`    | ✅ Supported | Requires If-Match ETag                | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_DeleteContinuousDeploymentPolicy.html)    |
| `ListContinuousDeploymentPolicies`    | ✅ Supported | MaxItems pagination                   | [docs](https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListContinuousDeploymentPolicies.html)    |

---

## Emulator extensions

Operations Overcast serves that appear in no AWS model, so no SDK calls them and no AWS reference page describes them. They sit outside the operation counts above.

| Operation      | Status       | Notes                                                                                                                                                                                                          |
| -------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ProxyRequest` | ✅ Supported | Path-pattern matching, origin forwarding (dialled locally when Overcast answers for the origin), GET response caching, CloudFront Functions, origin-group failover, geo restriction and custom error responses |

## Related

- [CloudFront](../cloudfront.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
