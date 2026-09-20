---
title: "Backup — AWS Backup"
description: "Quick start, the vault, plan, access point and tag operations that work, and everything that never runs: no backup or restore jobs, no recovery points, no vault lock."
section: "Service Reference"
tags:
  - aws
  - backup
  - docs
  - services
---

# Backup — AWS Backup

Vaults, plans and backup access points exist so IaC that declares them deploys;
no backup job, restore job or schedule ever runs.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws backup create-backup-vault --backup-vault-name nightly
aws backup create-backup-plan --backup-plan '{
  "BackupPlanName": "nightly",
  "Rules": [{
    "RuleName": "daily",
    "TargetBackupVaultName": "nightly",
    "ScheduleExpression": "cron(0 5 ? * * *)"
  }]
}'
aws backup list-backup-vaults
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Vaults | Create, describe, list, delete, per region |
| Plans | Create, get, list, update, delete; an update mints a new `VersionId` |
| Rules | Stored and echoed back in full, including schedules and lifecycle blocks |
| Access points | Create, describe, list, delete; the two filtered listings match on stored metadata |
| Tags | `BackupVaultTags`, `BackupPlanTags` and an access point's `Tags` at creation, plus `TagResource`, `ListTags` and `UntagResource` |
| Bindings | AWS's own routes — vaults under `/backup-vaults`, plans under `/backup/plans`, access points under `/backup-access-point` — so SDKs, CDK and `aws backup …` work unmodified |
| Protocols | AWS REST JSON (Smithy `restJson1`), dispatched by each operation's modeled method and path |

## Differences from AWS

| Area                     | On AWS                                                           | Overcast                                                                                                 |
| ------------------------ | ---------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| Backup jobs              | Rules fire on schedule and produce backup, restore and copy jobs | Rules are stored, never fired; there are no jobs of any kind                                             |
| Recovery points          | Created and listed per vault                                     | A vault's `NumberOfRecoveryPoints` is always zero, and the recovery-point operations are not implemented |
| Vault lock               | `Locked` and the retention members                               | `Locked` is always false and the retention members are absent                                            |
| Shared vaults            | `ListBackupVaults --shared` lists them                           | Every vault is a standard unshared vault, so it lists none                                               |
| Plan versions            | Every version is retrievable by `VersionId`                      | Only the current version is kept — `GetBackupPlan` with an older `VersionId` is a miss                   |
| Deleted plans            | Tombstoned, so `ListBackupPlans --include-deleted` finds them    | Plans delete outright, so it adds nothing                                                                |
| `AdvancedBackupSettings` | Stored and returned                                              | Neither stored nor returned                                                                              |
| Backup access points     | Serve a recovery point's data through an S3 access point         | Metadata only — no S3 access point exists, so no `S3AccessPointArn` or `S3AccessPointAlias` comes back   |
| `AccessPointPolicy`      | Applied to the underlying S3 access point                        | Accepted and dropped; nothing here enforces an access policy                                             |
| Access point listings    | Find the access points on a recovery point or resource           | Both filter stored metadata, but no recovery point ever exists, so both come back empty                  |

## Gotchas

> [!NOTE]
> Tags are stored inline on the vault, plan or access point record, so deleting
> the resource deletes its tags in the same write. Nothing outlives the record
> it describes.

<!-- BEGIN overcast:capabilities -->

## Operations

All 18 listed operations are implemented.
Per-operation status, notes and AWS API links: [Backup operations](backup/operations.md).

<!-- END overcast:capabilities -->

## Related

- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/aws-backup/latest/devguide/API_Reference.html)
