---
title: "Local VPCs for CDK"
description: "Give the local environment a stack that creates the VPC: CDK owns the IDs, a teardown just produces new ones, and the application stacks never learn which environment they are in."
section: "CDK"
tags:
  - cdk
  - docs
  - local
  - vpc
  - vpcs
---

# Local VPCs for CDK

Overcast's local VPCs get new `vpc-*`, `subnet-*` and `rtb-*` IDs on every
teardown, so nothing outside [a CDK deploy](../cdk.md) can track them without
going stale. Give the local environment its own stack that **creates** the VPC:
CDK owns the IDs, and a teardown/redeploy cycle produces new ones it already
knows.

## The pattern

1. Put local-only resources in their own stack — a `LocalResourcesStack` that creates the VPC.
2. Have the local stage create that stack and pass its VPC to the application stacks.
3. Keep application stacks environment-agnostic: they accept an `ec2.IVpc` and never decide where it came from.
4. For real AWS environments, have the stage supply the VPC from a stack that calls `Vpc.fromLookup` — see [Importing a VPC into CDK](./vpc-lookups.md).

The environment-specific decision lives in one place — which stack the stage
creates — and every other stack is written once.

## The local resources stack

```typescript
import * as cdk from "aws-cdk-lib";
import * as ec2 from "aws-cdk-lib/aws-ec2";
import type { Construct } from "constructs";

export class LocalResourcesStack extends cdk.Stack {
  readonly vpc: ec2.IVpc;

  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    this.vpc = new ec2.Vpc(this, "Vpc", {
      maxAzs: 2,
      natGateways: 1,
      subnetConfiguration: [
        { name: "Public", subnetType: ec2.SubnetType.PUBLIC, cidrMask: 24 },
        {
          name: "Private",
          subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS,
          cidrMask: 24,
        },
      ],
    });
  }
}
```

An ordinary CDK stack, deployed by `cdk deploy` like any other. It uses only
resource types Overcast supports through CloudFormation — `AWS::EC2::VPC`,
`Subnet`, `RouteTable`, `Route`, `SubnetRouteTableAssociation`,
`InternetGateway`, `VPCGatewayAttachment`, `NatGateway` and `EIP`.

Creating the VPC also emits the subnet metadata CDK later relies on: each
subnet is tagged `aws-cdk:subnet-type` and `aws-cdk:subnet-name`, public
subnets get a `0.0.0.0/0` route to the internet gateway, and private subnets get
one to the NAT gateway. Constructs that default to private subnets — scheduled
Fargate tasks, VPC-attached Lambda functions — find a private-with-egress subnet
group with no extra tagging work. `allowPublicSubnet: true` silences the Lambda
placement warning without fixing connectivity: a function in a public subnet
gets no public IP, and private-with-egress is the placement the warning is
asking for.

> [!NOTE]
> Overcast stores NAT gateways and route tables as metadata. They are enough for
> CDK's subnet classification, but they do not imply real NAT data-plane
> routing. See [EC2 limitations](../services/ec2.md#differences-from-aws).

## Wiring it up in the stage

The stage picks which stack supplies the VPC:

```typescript
class LocalStage extends cdk.Stage {
  constructor(scope: Construct, id: string) {
    // No env — see "Availability zones" in vpc-lookups.md.
    super(scope, id);

    const resources = new LocalResourcesStack(this, "LocalResources");

    new WorkerStack(this, "Worker", { vpc: resources.vpc });
    new ApiStack(this, "Api", { vpc: resources.vpc });
  }
}

class ProdStage extends cdk.Stage {
  constructor(scope: Construct, id: string, props: ProdStageProps) {
    super(scope, id, props);

    const network = new NetworkStack(this, "Network", { vpcId: props.vpcId });

    new WorkerStack(this, "Worker", { vpc: network.vpc });
    new ApiStack(this, "Api", { vpc: network.vpc });
  }
}
```

`NetworkStack` is the real-AWS counterpart, and it is just as small —
[Importing a VPC into CDK](./vpc-lookups.md) has it.

Passing a VPC between stacks in the same stage is ordinary CDK: the consuming
stack references it through `Fn::ImportValue`, and CDK adds the stack dependency
for you. Overcast's CloudFormation engine resolves cross-stack exports — see the
[CloudFormation service reference](../services/cloudformation.md). Weak
references, and stacks in different regions, are covered in
[Cross-stack references in CDK](./cross-stack-references.md).

## Application stacks stay environment-agnostic

```typescript
interface WorkerStackProps extends cdk.StackProps {
  vpc: ec2.IVpc;
}

class WorkerStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: WorkerStackProps) {
    super(scope, id, props);

    // Use props.vpc normally in Lambda, ECS, RDS, and other constructs.
  }
}
```

No `isLocal` flag, no branch on account or region, no local metadata file to
load. The stack is identical in every environment; only the stage differs.

> [!WARNING]
> A VPC construct has to live under a `Stack`. Creating one directly in a stage
> fails synth with `should be created in the scope of a Stack, but no Stack
> found`, and `Vpc.fromVpcAttributes` at stage scope fails the same way. The
> stage owns the local/prod decision; a stack owns the constructs.

## Related

- [Importing a VPC into CDK](./vpc-lookups.md) — lookups, availability zones, and a VPC created outside CDK
- [CDK resource type coverage](./resource-types.md) — the EC2 and VPC types this pattern provisions
- [Using AWS CDK](../cdk.md) — bootstrap and deploy CDK stacks against Overcast
- [EC2 / VPC service reference](../services/ec2.md) — VPC support, Docker-backed network behaviour, and limitations
- [CloudFormation service reference](../services/cloudformation.md) — cross-stack exports and resource provisioning
- [Reference index](../README.md) — every guide and service page
