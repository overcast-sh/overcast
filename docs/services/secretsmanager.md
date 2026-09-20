---
title: "Secrets Manager — AWS Secrets Manager"
description: "Quick start, versions and staging labels, the four rotation steps run against your Lambda, the deletion recovery window, and the encryption and resource policies that are not real."
section: "Service Reference"
tags:
  - docs
  - manager
  - secrets
  - secretsmanager
  - services
---

# Secrets Manager — AWS Secrets Manager

Versioned secrets with real staging labels, and rotation that actually invokes
your rotation Lambda. Values are stored in plaintext.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws secretsmanager create-secret --name app/db \
  --secret-string '{"username":"app","password":"hunter2"}'

aws secretsmanager get-secret-value --secret-id app/db \
  --query SecretString --output text
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area            | Behaviour                                                                                              |
| --------------- | -------------------------------------------------------------------------------------------------------- |
| Secrets         | String and binary values, description, tags, and a recorded `KmsKeyId`                                   |
| Lookup          | By name, full ARN, or the partial ARN without the six-character suffix — all three, as on AWS            |
| Versions        | `ClientRequestToken` becomes the version ID; `VersionStages` defaults to `AWSCURRENT`, and taking it off a version moves `AWSPREVIOUS` onto it |
| Rotation        | `RotateSecret` runs AWS's four steps against your Lambda; `RotationRules` schedules drive a background loop |
| Password generation | `GetRandomPassword` honours `PasswordLength`, the `Exclude*` settings, `IncludeSpace` and `RequireEachIncludedType` |
| Resource policies | Put, get, delete and syntactic validation, with `BlockPublicPolicy` honoured                            |
| Batch reads     | `BatchGetSecretValue`, with partial results when a secret is missing                                     |
| Deletion        | `DeleteSecret` schedules a delete 7-30 days out (30 by default); the secret is hidden, refuses value operations, and `RestoreSecret` cancels it until the window closes |

## Rotation

`RotateSecret` invokes the configured function once per step —
`createSecret`, `setSecret`, `testSecret`, `finishSecret` — with the payload
AWS sends (`{"Step", "SecretId", "ClientRequestToken"}`) and the same token
throughout. Before the first invocation the token is made a version staged
`AWSPENDING` with no value in it, exactly as AWS does, so a rotation function
copied from an AWS blueprint works unmodified.

Rotation itself diverges in two places:

| Area            | On AWS                                              | Overcast                                                    |
| --------------- | --------------------------------------------------- | ------------------------------------------------------------- |
| Timing          | Returns immediately, rotates in the background      | Returns once the sequence has finished                        |
| A failed step   | Answers `200`; the failure surfaces in CloudTrail   | `InvalidRequestException` naming the step, staging labels left as the function left them |

`RotateSecret` with no rotation function configured — neither on the call nor
already on the secret — is `InvalidRequestException`, as on AWS.

## Differences from AWS

| Area               | On AWS                                         | Overcast                                                                                                |
| ------------------ | ---------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Encryption at rest | Envelope-encrypted under the named KMS key     | `KmsKeyId` is recorded as metadata; the value is stored in plaintext                                    |
| Permanent deletion | A background task clears the secret some time after the window closes | The record stays until something reads it, so a name frees up the instant the window closes |
| Resource policies  | Evaluated on every call                        | Stored and syntax-checked; never evaluated ([#496](https://github.com/overcast-sh/overcast/issues/496)) |
| Replication        | `ReplicateSecretToRegions` and its counterpart | Not implemented — `501 Not Implemented`                                                                 |

## Gotchas

> [!NOTE]
> A version that exists but holds no value — the state a rotation leaves
> between staging `AWSPENDING` and the function writing to it — is listed by
> `DescribeSecret` but reported as `ResourceNotFoundException` by
> `GetSecretValue`. `PutSecretValue` under that token fills it rather than
> answering `ResourceExistsException`.

<!-- BEGIN overcast:capabilities -->

## Operations

20 of 22 listed operations are implemented.
Per-operation status, notes and AWS API links: [Secrets Manager operations](secretsmanager/operations.md).

<!-- END overcast:capabilities -->

## Related

- [CloudFormation](./cloudformation.md) — `GenerateSecretString` generates through `GetRandomPassword`
- [KMS](./kms.md) — the key `KmsKeyId` records but never encrypts through
- [SSM Parameter Store](./ssm.md) — the simpler alternative
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/secretsmanager/latest/apireference/Welcome.html)
