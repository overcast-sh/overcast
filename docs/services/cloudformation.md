---
title: "CloudFormation — AWS CloudFormation"
description: "Quick start, template and intrinsic coverage, the stack lifecycle and change sets, per-resource drift on update, and where an Overcast-prefixed status reason comes from."
section: "Service Reference"
tags:
  - cloudformation
  - docs
  - iac
  - services
---

# CloudFormation — AWS CloudFormation

Stacks, change sets and a provisioner that creates real resources by dispatching
through the emulator's own router — the entry point for CDK deploys.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

cat > bucket.yaml <<'YAML'
Resources:
  Data:
    Type: AWS::S3::Bucket
Outputs:
  Name: { Value: !Ref Data }
YAML

aws cloudformation create-stack --stack-name demo \
  --template-body file://bucket.yaml
aws cloudformation wait stack-create-complete --stack-name demo
aws cloudformation describe-stacks --stack-name demo --query 'Stacks[0].Outputs'
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Templates | JSON and YAML, including the short-form tags (`!Ref`, `!Sub`, `!GetAtt`). Both of AWS's size quotas are enforced — 51,200 bytes inline, 1,000,000 via `TemplateURL`. |
| Resource types | 137 registered types — 128 provisioned for real, 9 recognised as deliberate stubs — plus custom resources and nested stacks. The list is in [CDK resource type coverage](../cdk/resource-types.md). |
| Provisioning | Resources are created through internal HTTP requests to the emulated services, so anything implemented can be orchestrated. `DependsOn` and reference ordering are resolved by topological sort. |
| Stack lifecycle | The full AWS status machine, including both cleanup states, and enforced transitions — a create is refused while the name exists, an update only from a last-known-stable state. |
| Change sets | Create, describe, execute and list. `ExecuteChangeSet` advances `ExecutionStatus` to `EXECUTE_COMPLETE`/`EXECUTE_FAILED` when the stack reaches a terminal status. |
| Updates | Per-resource drift by hash of the resolved properties. Unchanged resources are skipped; changed ones are updated in place where the handler supports it, and replaced otherwise. |
| Rollback | Automatic on failure, plus `RollbackStack` and `ContinueUpdateRollback` (including `ResourcesToSkip`, with AWS's nested `Stack.Logical` paths). An update rollback restores the template, parameters and tags as well as the resources. |
| Resources that wait | Twelve types stabilise before completing — RDS instances and clusters, ECS services, three ElastiCache types, MSK, EKS, three EFS types and Lambda functions — so `CREATE_COMPLETE` means settled, not merely accepted. |
| Nested stacks and custom resources | `AWS::CloudFormation::Stack` fetches and provisions the child synchronously; `Custom::*` invokes the Lambda named by `ServiceToken` and reads back `PhysicalResourceId` and `Data`. |
| Cross-stack references | `Fn::ImportValue` resolves exports from other active stacks in the same region, and `ListExports` and `ListImports` return the index; `Fn::GetStackOutput` reads any output of a stack in any region, exported or not — see [Cross-stack references in CDK](../cdk/cross-stack-references.md). |

Intrinsics: `Ref`, `Fn::Sub`, `Fn::Join`, `Fn::Select`, `Fn::GetAtt`, `Fn::If`,
`Fn::Split`, `Fn::GetAZs`, `Fn::ImportValue`, `Fn::GetStackOutput`, `Fn::FindInMap`,
`Fn::Base64`, `Fn::Cidr`, `Fn::Equals`, `Fn::Not`, `Fn::And`, `Fn::Or`. Pseudo-parameters:
`AWS::Region`, `AWS::AccountId`, `AWS::StackId`, `AWS::StackName`,
`AWS::URLSuffix`, `AWS::Partition`, `AWS::NotificationARNs`, `AWS::NoValue`.

## Differences from AWS

| Area                                                | On AWS                                         | Overcast                                                                                         |
| --------------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `AWS::NoValue`                                      | Removes the property                           | Substitutes the empty string                                                                     |
| `{{resolve:s3:...}}`                                | Resolved                                       | Not resolved; fails the resource                                                                 |
| SSM dynamic-reference versions                      | Version-selected                               | An explicit version resolves to the current value, with a warning                                |
| Unknown resource types                              | Rejected                                       | Accepted with a synthetic stub ID, a warning, and an `Overcast:`-prefixed `ResourceStatusReason` |
| `DeleteStack`'s `RetainResources`                   | Supported                                      | Not implemented                                                                                  |
| `DeletionPolicy: Snapshot`                          | Snapshots                                      | Treated as `Retain`; no snapshot is taken                                                        |
| Drift detection, StackSets, stack policies, imports | Supported                                      | Not implemented                                                                                  |
| `Fn::GetStackOutput` where AWS does not yet allow it | `InternalFailure` as a direct output value or inside `Fn::Sub` variables, `Fn::Base64`, `Fn::Equals`, `Fn::ImportValue` | Resolved wherever it appears |
| `Fn::GetStackOutput` with a `RoleArn`               | Assumes the role to read another account's stack | Shape-checked only; the one emulated account is read |

The full list is in [Limitations](./cloudformation/limitations.md), with the
status machine and the waits. Behind it:
[stack updates](./cloudformation/updates.md),
[teardown](./cloudformation/teardown.md) and
[dynamic references](./cloudformation/dynamic-references.md).

## Gotchas

> [!IMPORTANT]
> A stack can reach `CREATE_COMPLETE` while parts of it do nothing — an
> unregistered type, a deliberate no-op handler, or a real resource whose owning
> service is registered at inert or stub tier. Each says so in its
> `ResourceStatusReason`, prefixed `Overcast:`, on the event a `cdk deploy` is
> already polling. The console colours those as notices and badges the stack
> "N of M stub or inert".

A stack that fails outright leaves a separate trail.

> [!TIP]
> When a deploy fails, the rollback destroys the evidence. Overcast reads it
> first — `GET /_overcast/cloudformation/stacks/{stackName}/diagnostics` returns
> the CloudFormation reason plus what the failing resource's own service knew,
> each pane tagged with where it came from. `AWS::ECS::Service` is the resource
> type covered today; a healthy stack answers `404`.

<!-- BEGIN overcast:capabilities -->

## Operations

25 of 53 listed operations are implemented.
Per-operation status, notes and AWS API links: [CloudFormation operations](cloudformation/operations.md).

<!-- END overcast:capabilities -->

## Related

- [CloudFormation limitations](./cloudformation/limitations.md) — every divergence, the status machine, the waits
- [CloudFormation troubleshooting](./cloudformation/troubleshooting.md) — stuck stacks and failed deploys
- [CloudFormation stack updates](./cloudformation/updates.md) — in-place, replace, and what a rollback puts back
- [CloudFormation teardown](./cloudformation/teardown.md) — failed deletes and DeletionPolicy
- [CloudFormation dynamic references](./cloudformation/dynamic-references.md) — resolve semantics
- [Cross-stack references in CDK](../cdk/cross-stack-references.md) — strong and weak references, stacks in different regions
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [CDK](../cdk.md) — the supported resource types and how to point CDK here
- [AWS API reference](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/Welcome.html)
