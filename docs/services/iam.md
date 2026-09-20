---
title: "IAM — Identity and Access Management"
description: "Quick start, the entities and policy simulator that work, what OVERCAST_ENFORCE_IAM evaluates and the action prefixes it uses, and what is never enforced."
section: "Service Reference"
tags:
  - access
  - docs
  - iam
  - identity
  - management
  - services
---

# IAM — Identity and Access Management

Users, roles, groups, policies and instance profiles, so CDK and Terraform
stacks provision. Nothing is enforced unless you switch enforcement on.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws iam create-role --role-name app \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

aws iam attach-role-policy --role-name app \
  --policy-arn arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

> [!CAUTION]
> **Overcast is not a security boundary.** Credentials are accepted but never
> verified, and every call succeeds regardless of attached policies unless you
> opt in to enforcement below.

## What works

| Area              | Behaviour                                                                                     |
| ----------------- | ----------------------------------------------------------------------------------------------- |
| Entities          | Users, roles, groups, managed and inline policies, instance profiles, access keys, tags on all of them |
| Policy documents  | Parsed before they are stored — every operation that takes one refuses a malformed document with `MalformedPolicyDocument` (400) |
| Policy usage      | `GetPolicy` and `ListPolicies` count real attachments in `AttachmentCount`, and bounded entities in `PermissionsBoundaryUsageCount` |
| Tags              | Applied inline at create, changed with `Tag*`/`Untag*`, and returned on the resource by `GetRole`, `GetUser`, `GetPolicy` and `GetInstanceProfile` — the `List*` operations omit them, as AWS does |
| Group membership  | `GetGroup` resolves members into `Users`, paginated with `Marker` / `MaxItems` (default 100, cap 1000) |
| Permissions boundaries | Attached at create or later, reported as `AttachedPermissionsBoundary`, and read by both the simulator and enforcement |
| Policy simulation | `SimulateCustomPolicy` and `SimulatePrincipalPolicy` run a real evaluation and return AWS's `allowed` / `explicitDeny` / `implicitDeny` vocabulary |
| Delete safety     | `DeleteUser`, `DeleteRole`, `DeleteGroup`, `DeletePolicy` and `DeleteInstanceProfile` refuse with `DeleteConflict` (409) while dependencies remain |
| Account details   | `GetAccountAuthorizationDetails`                                                              |

Simulation reads nothing else and changes nothing, and it works whether or not
enforcement is switched on. `MatchedStatements` names the deciding policy and
statement, with `StartPosition` / `EndPosition` pointing at that statement's
braces in the exact document text supplied, so two statements in the *same*
document are told apart.

## Request-time enforcement (opt-in)

Set `OVERCAST_ENFORCE_IAM=true` and every request is evaluated against the
calling principal's policies before it reaches the service handler. **It is off
by default, and with it off the evaluator reads nothing and decides nothing.**

When it is on:

- The caller is resolved from the SigV4 access key to an IAM user (its inline,
  attached and group policies) or to a role assumed through STS, plus that
  entity's permissions boundary.
- A refused request gets the calling service's own `AccessDenied`-shaped error,
  in that service's wire format.
- **Identity policies only.** Resource-based policies — S3 bucket policies,
  Lambda/SQS/SNS policies — are not consulted. Pass one to the simulator
  explicitly to test it.
- Enforcement is **fail-closed**: an unsigned request, an unparseable policy, or
  a construct the evaluator does not implement all deny.

The action evaluated is `<prefix>:<Operation>`, where the prefix is the IAM
action prefix AWS itself uses — so write policies with the names the AWS
documentation gives. Ten services differ from their Overcast service key:

| Service            | IAM action prefix       |
| ------------------ | ------------------------- |
| MSK                | `kafka:`                  |
| Step Functions     | `states:`                 |
| EFS                | `elasticfilesystem:`      |
| OpenSearch         | `es:`                     |
| ELBv2              | `elasticloadbalancing:`   |
| AppRegistry        | `servicecatalog:`         |
| Cognito user pools | `cognito-idp:`            |
| WAF                | `wafv2:`                  |
| DynamoDB Streams   | `dynamodb:`               |
| AppConfig Data     | `appconfig:`              |

## Differences from AWS

| Area                                                                         | On AWS                                    | Overcast                                                                              |
| ---------------------------------------------------------------------------- | ----------------------------------------- | ------------------------------------------------------------------------------------- |
| Enforcement                                                                  | Always on                                 | Off unless `OVERCAST_ENFORCE_IAM=true`; identity policies only                        |
| Credentials                                                                  | Verified against the signing key          | Accepted without verification                                                         |
| Policy versions                                                              | Every version is retained and retrievable | A counter only — no `GetPolicyVersion`, `ListPolicyVersions` or `DeletePolicyVersion` |
| Policy document validation                                                   | The full policy grammar                   | Structure only — see [Limitations](./iam/limitations.md#policy-documents-are-checked-at-the-api-boundary) |
| Login profiles, MFA devices, SSH keys, signing certificates, Git credentials | Full API                                  | Not modelled                                                                          |

The policy language the evaluator does and does not cover is in
[Limitations](./iam/limitations.md).

## Gotchas

> [!WARNING]
> A teardown script that deletes an IAM entity without unwinding it first now
> gets a `409 DeleteConflict`, as it would against real AWS. See
> [Troubleshooting](./iam/troubleshooting.md).

<!-- BEGIN overcast:capabilities -->

## Operations

All 74 listed operations are implemented.
Per-operation status, notes and AWS API links: [IAM operations](iam/operations.md).

<!-- END overcast:capabilities -->

## Related

- [IAM limitations](./iam/limitations.md)
- [IAM troubleshooting](./iam/troubleshooting.md)
- [STS](./sts.md) — where an assumed-role session comes from
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/IAM/latest/APIReference/Welcome.html)
