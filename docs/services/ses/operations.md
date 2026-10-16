---
title: "SES operations"
description: "Every SES operation Overcast declares — 27 of 45 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - operations
  - services
  - ses
---

<!-- BEGIN overcast:capabilities -->

# SES operations

27 of 45 listed operations are implemented. Back to [SES](../ses.md).

## Summary

| Category            | ✅ Supported | ❌ Unsupported |
| ------------------- | ------------ | -------------- |
| SES v1 — Sending    | 2            |                |
| SES v1 — Identities | 9            | 7              |
| SES v1 — Templates  | 6            |                |
| SES v1 — Other      | 2            | 10             |
| SES v2 — Sending    | 1            |                |
| SES v2 — Identities | 4            |                |
| SES v2 — Tags       | 3            |                |
| SES v2 — Other      |              | 1              |

---

## Endpoints

### SES v1 — Sending

| Operation      | Status       | Notes                                    | AWS Docs                                                                          |
| -------------- | ------------ | ---------------------------------------- | --------------------------------------------------------------------------------- |
| `SendEmail`    | ✅ Supported | Query `Action=SendEmail`; simple content | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SendEmail.html)    |
| `SendRawEmail` | ✅ Supported | Delivers raw MIME to mail capture        | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SendRawEmail.html) |

### SES v1 — Identities

| Operation                              | Status         | Notes                              | AWS Docs                                                                                                  |
| -------------------------------------- | -------------- | ---------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `VerifyEmailIdentity`                  | ✅ Supported   | Auto-verified                      | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_VerifyEmailIdentity.html)                  |
| `VerifyDomainIdentity`                 | ✅ Supported   | Auto-verified; returns dummy token | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_VerifyDomainIdentity.html)                 |
| `ListIdentities`                       | ✅ Supported   | Supports `IdentityType` filter     | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_ListIdentities.html)                       |
| `ListVerifiedEmailAddresses`           | ✅ Supported   |                                    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_ListVerifiedEmailAddresses.html)           |
| `GetIdentityVerificationAttributes`    | ✅ Supported   | Always returns `Success` status    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetIdentityVerificationAttributes.html)    |
| `DeleteIdentity`                       | ✅ Supported   |                                    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DeleteIdentity.html)                       |
| `VerifyEmailAddress`                   | ✅ Supported   |                                    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_VerifyEmailAddress.html)                   |
| `DeleteVerifiedEmailAddress`           | ✅ Supported   |                                    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DeleteVerifiedEmailAddress.html)           |
| `SetIdentityFeedbackForwardingEnabled` | ✅ Supported   |                                    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SetIdentityFeedbackForwardingEnabled.html) |
| `GetIdentityDkimAttributes`            | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetIdentityDkimAttributes.html)            |
| `GetIdentityMailFromDomainAttributes`  | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetIdentityMailFromDomainAttributes.html)  |
| `GetIdentityNotificationAttributes`    | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetIdentityNotificationAttributes.html)    |
| `SetIdentityDkimEnabled`               | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SetIdentityDkimEnabled.html)               |
| `SetIdentityMailFromDomain`            | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SetIdentityMailFromDomain.html)            |
| `SetIdentityNotificationTopic`         | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SetIdentityNotificationTopic.html)         |
| `VerifyDomainDkim`                     | ❌ Unsupported | stub; returns 501                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_VerifyDomainDkim.html)                     |

### SES v1 — Templates

| Operation            | Status       | Notes                           | AWS Docs                                                                                |
| -------------------- | ------------ | ------------------------------- | --------------------------------------------------------------------------------------- |
| `CreateTemplate`     | ✅ Supported |                                 | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_CreateTemplate.html)     |
| `GetTemplate`        | ✅ Supported |                                 | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetTemplate.html)        |
| `UpdateTemplate`     | ✅ Supported |                                 | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_UpdateTemplate.html)     |
| `ListTemplates`      | ✅ Supported |                                 | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_ListTemplates.html)      |
| `DeleteTemplate`     | ✅ Supported |                                 | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DeleteTemplate.html)     |
| `SendTemplatedEmail` | ✅ Supported | `{{key}}` variable substitution | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_SendTemplatedEmail.html) |

### SES v1 — Other

| Operation                | Status         | Notes                     | AWS Docs                                                                                    |
| ------------------------ | -------------- | ------------------------- | ------------------------------------------------------------------------------------------- |
| `GetSendQuota`           | ✅ Supported   | Returns unlimited quota   | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetSendQuota.html)           |
| `GetSendStatistics`      | ✅ Supported   | Returns empty data points | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_GetSendStatistics.html)      |
| `CreateConfigurationSet` | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_CreateConfigurationSet.html) |
| `DeleteConfigurationSet` | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DeleteConfigurationSet.html) |
| `ListConfigurationSets`  | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_ListConfigurationSets.html)  |
| `CreateReceiptRule`      | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_CreateReceiptRule.html)      |
| `CreateReceiptRuleSet`   | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_CreateReceiptRuleSet.html)   |
| `DeleteReceiptRule`      | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DeleteReceiptRule.html)      |
| `DeleteReceiptRuleSet`   | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DeleteReceiptRuleSet.html)   |
| `DescribeReceiptRule`    | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DescribeReceiptRule.html)    |
| `DescribeReceiptRuleSet` | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_DescribeReceiptRuleSet.html) |
| `ListReceiptRuleSets`    | ❌ Unsupported | stub; returns 501         | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_ListReceiptRuleSets.html)    |

### SES v2 — Sending

| Operation   | Status       | Notes                                            | AWS Docs                                                                          |
| ----------- | ------------ | ------------------------------------------------ | --------------------------------------------------------------------------------- |
| `SendEmail` | ✅ Supported | `POST /v2/email/outbound-emails`; simple content | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_SendEmail.html) |

### SES v2 — Identities

| Operation             | Status       | Notes                                                                            | AWS Docs                                                                                    |
| --------------------- | ------------ | -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `CreateEmailIdentity` | ✅ Supported | `POST /v2/email/identities`; auto-verified; inline `Tags` applied at creation    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_CreateEmailIdentity.html) |
| `ListEmailIdentities` | ✅ Supported | `POST /v2/email/list-identities` (and `GET /v2/email/identities` for older SDKs) | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_ListEmailIdentities.html) |
| `GetEmailIdentity`    | ✅ Supported | `GET /v2/email/identities/{EmailIdentity}`; reports the identity's `Tags`        | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_GetEmailIdentity.html)    |
| `DeleteEmailIdentity` | ✅ Supported | `DELETE /v2/email/identities/{EmailIdentity}`                                    | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_DeleteEmailIdentity.html) |

### SES v2 — Tags

| Operation             | Status       | Notes                                                                            | AWS Docs                                                                                    |
| --------------------- | ------------ | -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported | `POST /v2/email/tags`; email identity ARNs — configuration sets are not emulated | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported | `DELETE /v2/email/tags?ResourceArn=…&TagKeys=…`                                  | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported | `GET /v2/email/tags?ResourceArn=…`                                               | [docs](https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_ListTagsForResource.html) |

### SES v2 — Other

| Operation                 | Status         | Notes                    | AWS Docs                                                                     |
| ------------------------- | -------------- | ------------------------ | ---------------------------------------------------------------------------- |
| `All other v2 operations` | ❌ Unsupported | Returns `NotImplemented` | [docs](https://docs.aws.amazon.com/ses/latest/APIReference/API_V2Other.html) |

## Related

- [SES](../ses.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
