---
title: "Cross-stack references in CDK"
description: "Strong references (Fn::ImportValue) and weak ones (Fn::GetStackOutput) against Overcast: what each strength deploys as, stacks in different regions, what a dangling reference does, and the one account there is."
section: "CDK"
tags:
  - cdk
  - cloudformation
  - docs
  - overcast
---

# Cross-stack references in CDK

When one CDK stack uses a resource another stack defines — a bucket, a VPC, a
queue — the CDK synthesises a cross-stack reference. Each reference has a
[strength](https://docs.aws.amazon.com/cdk/v2/guide/resources.html#resources-reference-strength),
and the strength decides which CloudFormation mechanism the templates use.
Overcast deploys both.

| Strength             | Producer template             | Consumer template    | Coupling                                                        |
| -------------------- | ----------------------------- | -------------------- | --------------------------------------------------------------- |
| Strong (the default) | An `Output` with an `Export`  | `Fn::ImportValue`    | The export cannot go while any stack imports it                 |
| Weak                 | A plain `Output`              | `Fn::GetStackOutput` | None — either stack can be updated or deleted on its own        |
| Both                 | An `Output` with an `Export`  | `Fn::GetStackOutput` | Transitional: the export stays for consumers not yet moved over |

`Fn::ImportValue` reads the export index of the consuming stack's region —
the one `ListExports` returns. `Fn::GetStackOutput` reads the named output
straight off the producing stack, in whichever region the reference names,
whether or not the output is exported. Strength is chosen in the app —
app-wide through the `@aws-cdk/core:defaultCrossStackReferences` context key,
per resource with `CrossStackReferences.of(resource).produce(strength)`, or
per usage with `Stack.consumeReference()` — and nothing about Overcast changes
which one to pick; the CDK guide linked above covers that, including the
two-phase migration from strong to weak.

## Stacks in different regions

A stack in `us-east-1` that uses a resource from a stack in `us-west-2` used
to need `crossRegionReferences: true`, which synthesised a custom resource
that copied the value through SSM parameters. The CDK now emits
`Fn::GetStackOutput` with a `Region` instead, and Overcast resolves it: the
CDK signs each stack's requests for that stack's region, Overcast keeps
state per region, and the consumer's reference reads the producer's output
from the region it names.

Deploy in dependency order, as `cdk deploy --all` already does. The producer
has to be `CREATE_COMPLETE` before the consumer's reference can find its
output.

## What a dangling reference does

A weak reference is resolved each time the consuming stack is created or
updated, and never again in between. Deleting the producer, or renaming the
output, does nothing to a consumer that is already deployed — that is the
point of the weakness — but the consumer's next create or update fails, as
it does on AWS. The resource that made the reference fails, and its stack
event says what was missing:

```text
Fn::GetStackOutput: stack "Producer" does not exist in us-west-2
Fn::GetStackOutput: output "VpcID" not found on stack "Producer" in us-west-2 (has SubnetId, VpcId)
```

The stack then rolls back the way any failed resource rolls it back. A
reference in the `Outputs` section fails the stack the same way, attributed
to the output.

## One account

`RoleArn` is how a reference reaches a stack in another account: on AWS,
CloudFormation assumes that role to describe the producing stack. Overcast
checks that the value is shaped like an IAM role ARN and does nothing else
with it. There is one account (`OVERCAST_ACCOUNT_ID`) and the CDK will not
deploy to a second one against the same endpoint, so a template written for
two accounts and pointed here reads its producer from the one account there
is.

## Where Overcast is more lenient than AWS

AWS does not yet accept `Fn::GetStackOutput` in every position. As a direct
`Outputs` value, inside an `Fn::Sub` variable map, inside `Fn::Base64`,
inside `Fn::Equals` in `Conditions`, or inside `Fn::ImportValue`, the stack
operation fails with `InternalFailure`. Overcast resolves the reference
wherever it appears. A template that relies on one of those positions
deploys here and fails in the account until AWS lifts the limitation, which
its
[reference page](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-getstackoutput.html)
says it will.

## Related

- [Using AWS CDK with Overcast](../cdk.md) — bootstrap, deploy, and the rest of CDK
- [Local VPCs for CDK](./local-vpc.md) — passing a VPC between stacks in the same stage
- [CloudFormation service reference](../services/cloudformation.md) — the intrinsics Overcast resolves
- [Fn::GetStackOutput](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/intrinsic-function-reference-getstackoutput.html) — the AWS reference
