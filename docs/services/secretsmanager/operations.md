---
title: "Secrets Manager operations"
description: "Every Secrets Manager operation Overcast declares — 20 of 22 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - operations
  - secretsmanager
  - services
---

<!-- BEGIN overcast:capabilities -->

# Secrets Manager operations

20 of 22 listed operations are implemented. Back to [Secrets Manager](../secretsmanager.md).

## Summary

| Category    | ✅ Supported | ❌ Unsupported |
| ----------- | ------------ | -------------- |
| Secret CRUD | 10           |                |
| Rotation    | 3            |                |
| Tags        | 2            |                |
| Password    | 1            |                |
| Policy/Misc | 4            | 2              |

---

## Endpoints

### Secret CRUD

| Operation              | Status       | Notes                                                                                    | AWS Docs                                                                                             |
| ---------------------- | ------------ | ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `CreateSecret`         | ✅ Supported | String + binary, KMS key, tags, description                                              | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_CreateSecret.html)         |
| `GetSecretValue`       | ✅ Supported | By name, ARN, version ID, or stage                                                       | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_GetSecretValue.html)       |
| `DescribeSecret`       | ✅ Supported | Metadata, KMS key, tags, versions, rotation dates                                        | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DescribeSecret.html)       |
| `PutSecretValue`       | ✅ Supported | Staging labels + ClientRequestToken                                                      | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_PutSecretValue.html)       |
| `UpdateSecret`         | ✅ Supported | Description, KMS key + optional new value                                                | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_UpdateSecret.html)         |
| `ListSecrets`          | ✅ Supported | Sorted by name, KMS metadata, optional filters — Filter.Key validated against AWS's enum | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ListSecrets.html)          |
| `ListSecretVersionIds` | ✅ Supported | All versions with staging labels                                                         | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ListSecretVersionIds.html) |
| `DeleteSecret`         | ✅ Supported | Recovery window 7-30 days (default 30) or ForceDeleteWithoutRecovery                     | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DeleteSecret.html)         |
| `BatchGetSecretValue`  | ✅ Supported | Partial results on missing secrets; Filter.Key validated against AWS's enum              | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_BatchGetSecretValue.html)  |
| `RestoreSecret`        | ✅ Supported | Cancels a scheduled deletion inside the recovery window                                  | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_RestoreSecret.html)        |

### Rotation

| Operation                  | Status       | Notes                                       | AWS Docs                                                                                                 |
| -------------------------- | ------------ | ------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `RotateSecret`             | ✅ Supported | Invokes the rotation Lambda, all four steps | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_RotateSecret.html)             |
| `CancelRotateSecret`       | ✅ Supported | Turns rotation off, keeps the config        | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_CancelRotateSecret.html)       |
| `UpdateSecretVersionStage` | ✅ Supported | Moves staging labels between versions       | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_UpdateSecretVersionStage.html) |

### Tags

| Operation       | Status       | Notes                      | AWS Docs                                                                                      |
| --------------- | ------------ | -------------------------- | --------------------------------------------------------------------------------------------- |
| `TagResource`   | ✅ Supported | Merge/overwrite tags       | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_TagResource.html)   |
| `UntagResource` | ✅ Supported | Removes specified tag keys | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_UntagResource.html) |

### Password

| Operation           | Status       | Notes                                                      | AWS Docs                                                                                          |
| ------------------- | ------------ | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `GetRandomPassword` | ✅ Supported | Modeled length bounds, exclusions, RequireEachIncludedType | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_GetRandomPassword.html) |

### Policy/Misc

| Operation                      | Status         | Notes                                    | AWS Docs                                                                                                     |
| ------------------------------ | -------------- | ---------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `GetResourcePolicy`            | ✅ Supported   | Stored policy; not evaluated (#496)      | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_GetResourcePolicy.html)            |
| `PutResourcePolicy`            | ✅ Supported   | Validated + stored; not evaluated (#496) | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_PutResourcePolicy.html)            |
| `DeleteResourcePolicy`         | ✅ Supported   | Removes the stored policy                | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DeleteResourcePolicy.html)         |
| `ValidateResourcePolicy`       | ✅ Supported   | Syntax + schema checks, no evaluation    | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ValidateResourcePolicy.html)       |
| `ReplicateSecretToRegions`     | ❌ Unsupported | stub; returns 501                        | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ReplicateSecretToRegions.html)     |
| `RemoveRegionsFromReplication` | ❌ Unsupported | stub; returns 501                        | [docs](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_RemoveRegionsFromReplication.html) |

## Related

- [Secrets Manager](../secretsmanager.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
