---
title: "STS — Security Token Service"
description: "Quick start, what each credential call returns, how AssumeRole feeds opt-in IAM enforcement, and why nothing is ever verified."
section: "Service Reference"
tags:
  - docs
  - security
  - services
  - sts
  - token
---

# STS — Security Token Service

Every credential call succeeds and returns freshly generated fake credentials.
Nothing is verified — not the role, not the identity, not the token.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws sts get-caller-identity
aws sts assume-role \
  --role-arn arn:aws:iam::000000000000:role/app \
  --role-session-name local
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area                        | Behaviour                                                        |
| --------------------------- | ---------------------------------------------------------------- |
| `GetCallerIdentity`         | A fixed account, user ID and root ARN                            |
| `GetSessionToken`           | Temporary credentials (default 12 hours)                         |
| `AssumeRole`                | Temporary credentials plus an `AssumedRoleUser` (default 1 hour) |
| `AssumeRoleWithWebIdentity` | Temporary credentials; the token is not parsed                   |
| `GetFederationToken`        | Temporary credentials plus a `FederatedUser`                     |

Access keys are `ASIA`-prefixed, with a random secret and session token as on
AWS. `DurationSeconds` is honoured wherever it is accepted.

> [!NOTE]
> `AssumeRole` records the minted access key against the role ARN and session
> name, which is how opt-in [IAM enforcement](./iam.md#request-time-enforcement-opt-in)
> resolves a caller to a role's policies and names its session. With enforcement
> off, nothing reads it.

## Differences from AWS

| Area                                                                 | On AWS                                                  | Overcast                                       |
| -------------------------------------------------------------------- | ------------------------------------------------------- | ---------------------------------------------- |
| `AssumeRole`                                                         | The role must exist and its trust policy must allow you | Any `RoleArn` is accepted, existing or not     |
| `GetCallerIdentity`                                                  | Reports the actual signing principal                    | Always the account root ARN, whoever called    |
| Web identity tokens                                                  | The OIDC token is validated against the provider        | Not parsed; a fixed subject is returned        |
| Credential expiry                                                    | Expired credentials are refused                         | Never checked — no credential is ever verified |
| SAML, `AssumeRoot`, `DecodeAuthorizationMessage`, `GetAccessKeyInfo` | Full API                                                | Not implemented — `NotImplemented`             |

<!-- BEGIN overcast:capabilities -->

## Operations

5 of 11 listed operations are implemented.
Per-operation status, notes and AWS API links: [STS operations](sts/operations.md).

<!-- END overcast:capabilities -->

## Related

- [IAM](./iam.md) — where an assumed role's policies live
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/STS/latest/APIReference/welcome.html)
