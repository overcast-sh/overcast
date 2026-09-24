---
title: "KMS operations"
description: "Every KMS operation Overcast declares — 34 of 34 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - kms
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# KMS operations

All 34 listed operations are implemented. Back to [KMS](../kms.md).

## Summary

| Category          | ✅ Supported | ⚠️ Partial |
| ----------------- | ------------ | ---------- |
| Key lifecycle     | 7            | 1          |
| Aliases           | 4            |            |
| Symmetric crypto  | 6            | 1          |
| Asymmetric crypto | 4            |            |
| Tags              | 3            |            |
| Key policies      | 3            |            |
| Grants            | 5            |            |

---

## Endpoints

### Key lifecycle

| Operation              | Status       | Notes                                                                                                                                                                           | AWS Docs                                                                                  |
| ---------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `CreateKey`            | ⚠️ Partial   | Symmetric and RSA key specs; validates caller-safe custom policies unless bypassed; accepts `Tags`; rejects `Origin` other than `AWS_KMS` and `MultiRegion=true` (not emulated) | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateKey.html)            |
| `DescribeKey`          | ✅ Supported | Lookup by UUID, ARN, or alias                                                                                                                                                   | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_DescribeKey.html)          |
| `ListKeys`             | ✅ Supported | Excludes `PendingDeletion` keys; no pagination (Truncated=false)                                                                                                                | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListKeys.html)             |
| `EnableKey`            | ✅ Supported |                                                                                                                                                                                 | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_EnableKey.html)            |
| `DisableKey`           | ✅ Supported |                                                                                                                                                                                 | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_DisableKey.html)           |
| `UpdateKeyDescription` | ✅ Supported | Also dispatched by CloudFormation when AWS::KMS::Key Description changes                                                                                                        | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_UpdateKeyDescription.html) |
| `ScheduleKeyDeletion`  | ✅ Supported | `PendingWindowInDays` 7-30, defaulting to 30 and returned in the response; response `KeyId` is the key ARN                                                                      | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ScheduleKeyDeletion.html)  |
| `CancelKeyDeletion`    | ✅ Supported | Restores key to `Disabled` state; response `KeyId` is the key ARN                                                                                                               | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_CancelKeyDeletion.html)    |

### Aliases

| Operation     | Status       | Notes                                                                        | AWS Docs                                                                         |
| ------------- | ------------ | ---------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `CreateAlias` | ✅ Supported | `alias/` prefix required; reserved `alias/aws/` and duplicate names rejected | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateAlias.html) |
| `DeleteAlias` | ✅ Supported |                                                                              | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_DeleteAlias.html) |
| `ListAliases` | ✅ Supported | Optional `KeyId` filter (UUID, ARN, alias)                                   | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListAliases.html) |
| `UpdateAlias` | ✅ Supported | Updates target key for an existing alias                                     | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_UpdateAlias.html) |

### Symmetric crypto

| Operation                         | Status       | Notes                                                                                                                          | AWS Docs                                                                                             |
| --------------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| `Encrypt`                         | ✅ Supported | AES-256-GCM; `Plaintext` capped at 4096 bytes; ciphertext envelope includes key ID                                             | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_Encrypt.html)                         |
| `Decrypt`                         | ✅ Supported | Extracts key ID from ciphertext envelope; a `KeyId` naming a different key is an `IncorrectKeyException`                       | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_Decrypt.html)                         |
| `GenerateDataKey`                 | ✅ Supported | Exactly one of `KeySpec` (`AES_256`/`AES_128`) or `NumberOfBytes` (1-1024); returns plaintext + encrypted                      | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_GenerateDataKey.html)                 |
| `GenerateDataKeyWithoutPlaintext` | ✅ Supported | Same `KeySpec`/`NumberOfBytes` rules as `GenerateDataKey`; returns encrypted data key only                                     | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_GenerateDataKeyWithoutPlaintext.html) |
| `GenerateRandom`                  | ⚠️ Partial   | `NumberOfBytes` (1-1024) required; `CustomKeyStoreId` and `Recipient` are ignored (not emulated)                               | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_GenerateRandom.html)                  |
| `ReEncrypt`                       | ✅ Supported | Decrypts and re-encrypts ciphertext with destination key; a `SourceKeyId` naming a different key is an `IncorrectKeyException` | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ReEncrypt.html)                       |
| `GenerateDataKeyPair`             | ✅ Supported | RSA_2048, RSA_3072, RSA_4096 key pair specs                                                                                    | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_GenerateDataKeyPair.html)             |

### Asymmetric crypto

| Operation      | Status       | Notes                                                                                                                                 | AWS Docs                                                                          |
| -------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| `Sign`         | ✅ Supported | RSA_2048 keys with all six RSASSA_PSS_* / RSASSA_PKCS1_V1_5_* algorithms, RAW or DIGEST messages; other signing key specs are refused | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html)         |
| `Verify`       | ✅ Supported | RSA_2048 keys, every RSASSA algorithm; a signature that does not verify fails with KMSInvalidSignatureException                       | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_Verify.html)       |
| `GetPublicKey` | ✅ Supported | Returns DER-encoded public key for RSA keys                                                                                           | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_GetPublicKey.html) |
| `VerifyMac`    | ✅ Supported | HMAC_SHA_256, HMAC_SHA_384, HMAC_SHA_512                                                                                              | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_VerifyMac.html)    |

### Tags

| Operation          | Status       | Notes                      | AWS Docs                                                                              |
| ------------------ | ------------ | -------------------------- | ------------------------------------------------------------------------------------- |
| `TagResource`      | ✅ Supported | Add tags to a KMS key      | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_TagResource.html)      |
| `UntagResource`    | ✅ Supported | Remove tags from a KMS key | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_UntagResource.html)    |
| `ListResourceTags` | ✅ Supported | List tags for a KMS key    | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListResourceTags.html) |

### Key policies

| Operation         | Status       | Notes                                                                             | AWS Docs                                                                             |
| ----------------- | ------------ | --------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `GetKeyPolicy`    | ✅ Supported | Returns default or custom key policy                                              | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_GetKeyPolicy.html)    |
| `PutKeyPolicy`    | ✅ Supported | Validates policy structure, principals, and caller lockout safety before mutation | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_PutKeyPolicy.html)    |
| `ListKeyPolicies` | ✅ Supported | Returns list of policy names                                                      | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListKeyPolicies.html) |

### Grants

| Operation             | Status       | Notes                                                                   | AWS Docs                                                                                 |
| --------------------- | ------------ | ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `CreateGrant`         | ✅ Supported | Creates a grant with optional constraints and retiring principal        | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateGrant.html)         |
| `ListGrants`          | ✅ Supported | Lists grants with optional KeyId, GrantId, and GranteePrincipal filters | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListGrants.html)          |
| `RevokeGrant`         | ✅ Supported | Revokes a grant by ID                                                   | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_RevokeGrant.html)         |
| `RetireGrant`         | ✅ Supported | Retires a grant by ID or token                                          | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_RetireGrant.html)         |
| `ListRetirableGrants` | ✅ Supported | Lists grants retirable by a principal                                   | [docs](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListRetirableGrants.html) |

## Related

- [KMS](../kms.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
