---
title: "Using AWS CDK with Overcast"
description: "Bootstrap and deploy a CDK stack against Overcast: the environment CDK needs, the five calls a deploy makes, and where the resource coverage, limitations and fixes live."
section: "Getting Started"
tags:
  - aws
  - cdk
  - docs
  - overcast
---

# Using AWS CDK with Overcast

Overcast supports `cdk deploy` and `cdk destroy` for stacks built from the
[supported resource types](./cdk/resource-types.md).

> [!TIP]
> Deploying anything with a VPC? Read
> [Local VPCs for CDK](./cdk/local-vpc.md) first — local VPCs get new IDs on
> every teardown, and `Vpc.fromLookup` cannot track them.

## Quick start

### 1. Start Overcast

```bash
docker run --rm -p 4566:4566 ghcr.io/overcast-sh/overcast:latest
```

### 2. Configure environment

CDK needs credentials and an endpoint override:

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
```

### 3. Bootstrap (first time only)

CDK bootstrap creates an S3 bucket, SSM parameters, and IAM roles. Overcast
supports all of these:

```bash
npx cdk bootstrap aws://000000000000/us-east-1
```

The account ID `000000000000` matches Overcast's default (`OVERCAST_ACCOUNT_ID`).
If you've configured a different account ID, use that instead.

### 4. Deploy

```bash
npx cdk deploy --all --require-approval never
```

`--require-approval never` skips the interactive changeset review since there
are no real resources or costs involved.

### 5. Destroy

```bash
npx cdk destroy --all --force
```

## How it works

A deploy is five calls, and all of them are implemented:

| # | Call | What Overcast does |
| --- | --- | --- |
| 1 | `sts:GetCallerIdentity` | Answers with the configured `OVERCAST_ACCOUNT_ID` and `OVERCAST_DEFAULT_REGION` |
| 2 | `sts:AssumeRole` | Returns valid temporary credentials for the bootstrap roles; nothing is authenticated |
| 3 | S3 upload | Takes the synthesised template and assets into the CDK bootstrap bucket |
| 4 | `CreateChangeSet` / `ExecuteChangeSet` | Provisions resources by dispatching to the emulated services, on a background goroutine that outlives the call |
| 5 | `DescribeStacks` | Answers CDK's poll for `CREATE_COMPLETE` or `UPDATE_COMPLETE` |

Step 5 is a real poll. `OVERCAST_CFN_SYNC_WAIT_MS` (default 1000ms) is how long
Overcast waits so that a small stack is already terminal on the first poll; a
stack with more resources is still `*_IN_PROGRESS` when CDK starts asking.

## Example: deploy a Lambda + API Gateway stack

```typescript
import * as cdk from "aws-cdk-lib";
import * as lambda from "aws-cdk-lib/aws-lambda";
import * as apigw from "aws-cdk-lib/aws-apigateway";

const app = new cdk.App();
const stack = new cdk.Stack(app, "MyStack");

const fn = new lambda.Function(stack, "Handler", {
  runtime: lambda.Runtime.NODEJS_20_X,
  handler: "index.handler",
  code: lambda.Code.fromInline(`
    exports.handler = async () => ({
      statusCode: 200,
      body: JSON.stringify({ message: 'Hello from Overcast!' }),
    });
  `),
});

new apigw.LambdaRestApi(stack, "Api", { handler: fn });
```

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
npx cdk deploy --require-approval never
```

## The rest of CDK

| Page | Answers |
| --- | --- |
| [CDK resource type coverage](./cdk/resource-types.md) | Whether a construct provisions real state, is stubbed, or is unknown |
| [Local VPCs for CDK](./cdk/local-vpc.md) | A VPC whose IDs change on every teardown, and the stack pattern that survives it |
| [Importing a VPC into CDK](./cdk/vpc-lookups.md) | `Vpc.fromLookup`, availability zones, and a VPC something else created |
| [Cross-stack references](./cdk/cross-stack-references.md) | Strong and weak references, stacks in different regions, and what a dangling reference does |
| [CDK limitations](./cdk/limitations.md) | Custom resources, container assets, nested stacks, drift detection |
| [CDK troubleshooting](./cdk/troubleshooting.md) | A bootstrap that fails, a stack stuck in progress, the Windows asset upload |

## Related

- [CloudFormation service reference](./services/cloudformation.md) — the provisioner CDK deploys through
- [CloudFormation troubleshooting](./services/cloudformation/troubleshooting.md) — stuck stacks and failed deploys
- [Using AWS SDKs and CLI](./sdk-cli.md) — endpoint and credential configuration
- [Configuration](./configuration.md) — every environment variable
- [Hostnames that resolve for every caller](./networking/hostnames.md) — the wildcard-DNS name asset publishing needs
- [Reference index](./README.md) — every guide and service page
