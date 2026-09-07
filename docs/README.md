---
title: "Reference index"
description: "The index of every Overcast guide and reference, grouped by the job you are doing — getting running, building against it, tuning and inspecting it — with the per-service table underneath."
section: "Getting Started"
tags:
  - docs
  - documentation
---

# Reference index

Every guide and reference below, grouped by the job you are doing. To get a
container answering the AWS APIs first:

```bash
docker run --rm -p 4566:4566 ghcr.io/overcast-sh/overcast:latest
export AWS_ENDPOINT_URL=http://localhost:4566
aws s3 mb s3://demo
```

Credentials, regions and per-language client setup are in
[Using AWS SDKs and CLI](./sdk-cli.md).

## Get running

| Guide | For |
| --- | --- |
| [Install Overcast](./install.md) | The one-line installers for macOS, Linux and Windows, the Docker images, and installing by hand |
| [Using AWS SDKs and CLI](./sdk-cli.md) | Pointing the AWS CLI, Node.js, Python, Go, Java, .NET, Rust or Terraform at Overcast |
| [CLI reference](./cli.md) | Every `overcast` subcommand — background instances, introspection, AWS helpers, mDNS and TLS |
| [Using AWS CDK](./cdk.md) | `cdk bootstrap`, `cdk deploy`, which resource types provision for real, and the local-VPC pattern |
| [Testcontainers](./testcontainers.md) | Starting Overcast from integration tests |
| [Migrating from LocalStack](./migration-from-localstack.md) | Swapping the image and keeping your environment block |
| [LocalStack compatibility matrix](./localstack-compatibility.md) | Every port, URL, hostname, container convention and client tool, with its status |

## Build against it

| Guide | For |
| --- | --- |
| [Service reference](./services/README.md) | What each AWS service supports, operation by operation |
| [Networking and host-based addressing](./networking.md) | Host-routed endpoints, wildcard DNS, sibling containers, VPC isolation |
| [The inner loop](./local-dev.md) | Editing a file and seeing it take effect — `cdk watch`, Lambda and ECS hot reload |
| [Step debugging inside emulated compute](./debugger.md) | Attaching VS Code, a JetBrains IDE or Chrome DevTools to code running inside a Lambda function or ECS task |
| [HTTPS and HTTP/2](./https.md) | Browser-trusted TLS in two commands, in Docker, and by hand |

## Tune and inspect

| Guide | For |
| --- | --- |
| [Configuration](./configuration.md) | Where each setting lives, the handful most people change, and every variable with its default |
| [Storage and persistence](./storage.md) | Choosing a backend and knowing what survives a restart |
| [Performance](./performance.md) | Startup and memory expectations, storage tuning, and where "feels slow" time goes |
| [Debug endpoints](./debug-endpoints.md) | Health, metrics, state dump, request traces, pprof |
| [Troubleshooting](./troubleshooting.md) | A symptom, and where its answer lives |

---

## Runtime emulation tiers

`GET /_overcast/health`, `overcast services` and the web console report a tier
per running service. It answers "how much of this service is wired up", and is a
different axis from the coverage tier in the index below — see
[Service reference § Coverage tiers](./services/README.md#coverage-tiers) for
that one and for the per-operation status tokens.

| Tier        | Meaning                                                                                                                                                                           |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Full**    | P1+P2 operations implemented. Real SDK clients can use it end-to-end.                                                                                                             |
| **Partial** | P1 operations implemented. Basic workflows work.                                                                                                                                  |
| **Inert**   | Full CRUD works — resources are created and stored — but no side-effects or enforcement occur. For example, IAM stores users, roles, and policies but never enforces permissions. |
| **Stub**    | Registered so discovery works: at most a hardcoded, stateless answer to the service's describe call; every other operation returns `501 Not Implemented`.                        |

---

## Services

Shorter overview: the [service reference index](./services/README.md).

<!-- BEGIN overcast:service-index -->

| Service          | Doc                                                 | Ops | Coverage tier                 |
| ---------------- | --------------------------------------------------- | --- | ----------------------------- |
| S3               | [s3.md](./services/s3.md)                           | 53  | Comprehensive / broad support |
| SQS              | [sqs.md](./services/sqs.md)                         | 21  | Comprehensive / broad support |
| DynamoDB         | [dynamodb.md](./services/dynamodb.md)               | 28  | Comprehensive / broad support |
| Lambda           | [lambda.md](./services/lambda.md)                   | 62  | Comprehensive / broad support |
| API Gateway      | [apigateway.md](./services/apigateway.md)           | 106 | Comprehensive / broad support |
| AppSync          | [appsync.md](./services/appsync.md)                 | 82  | Comprehensive / broad support |
| CloudFront       | [cloudfront.md](./services/cloudfront.md)           | 89  | Comprehensive / broad support |
| Cognito          | [cognito.md](./services/cognito.md)                 | 70  | Comprehensive / broad support |
| EC2 / VPC        | [ec2.md](./services/ec2.md)                         | 79  | Comprehensive / broad support |
| SNS              | [sns.md](./services/sns.md)                         | 30  | Comprehensive / broad support |
| IAM              | [iam.md](./services/iam.md)                         | 74  | Core CRUD + common workflows  |
| ECS              | [ecs.md](./services/ecs.md)                         | 48  | Core CRUD + common workflows  |
| ECR              | [ecr.md](./services/ecr.md)                         | 22  | Core CRUD + common workflows  |
| KMS              | [kms.md](./services/kms.md)                         | 34  | Core CRUD + common workflows  |
| Kinesis          | [kinesis.md](./services/kinesis.md)                 | 23  | Core CRUD + common workflows  |
| EventBridge      | [eventbridge.md](./services/eventbridge.md)         | 29  | Core CRUD + common workflows  |
| Scheduler        | [scheduler.md](./services/scheduler.md)             | 12  | Core CRUD + common workflows  |
| CloudFormation   | [cloudformation.md](./services/cloudformation.md)   | 52  | Core CRUD + common workflows  |
| RDS              | [rds.md](./services/rds.md)                         | 34  | Core CRUD + common workflows  |
| ElastiCache      | [elasticache.md](./services/elasticache.md)         | 24  | Core CRUD + common workflows  |
| EFS              | [efs.md](./services/efs.md)                         | 31  | Core CRUD + common workflows  |
| AppConfig        | [appconfig.md](./services/appconfig.md)             | 20  | Core CRUD + common workflows  |
| AppConfigData    | [appconfigdata.md](./services/appconfigdata.md)     | 2   | Core CRUD + common workflows  |
| Secrets Manager  | [secretsmanager.md](./services/secretsmanager.md)   | 22  | Core CRUD + common workflows  |
| SSM              | [ssm.md](./services/ssm.md)                         | 18  | Core CRUD + common workflows  |
| CloudWatch Logs  | [cloudwatch-logs.md](./services/cloudwatch-logs.md) | 23  | Core CRUD + common workflows  |
| SES              | [ses.md](./services/ses.md)                         | 45  | Core CRUD + common workflows  |
| STS              | [sts.md](./services/sts.md)                         | 11  | Core CRUD + common workflows  |
| Route 53         | [route53.md](./services/route53.md)                 | 25  | Core CRUD + common workflows  |
| Auto Scaling     | [autoscaling.md](./services/autoscaling.md)         | 25  | Core CRUD + common workflows  |
| Step Functions   | [stepfunctions.md](./services/stepfunctions.md)     | 15  | Minimal / targeted support    |
| Pipes            | [pipes.md](./services/pipes.md)                     | 8   | Minimal / targeted support    |
| WAF v2           | [waf.md](./services/waf.md)                         | 7   | Minimal / targeted support    |
| Shield           | [shield.md](./services/shield.md)                   | 8   | Minimal / targeted support    |
| ACM              | [acm.md](./services/acm.md)                         | 11  | Minimal / targeted support    |
| Athena           | [athena.md](./services/athena.md)                   | 11  | Minimal / targeted support    |
| Bedrock          | [bedrock.md](./services/bedrock.md)                 | 2   | Minimal / targeted support    |
| CloudWatch       | [cloudwatch.md](./services/cloudwatch.md)           | 17  | Minimal / targeted support    |
| DynamoDB Streams | [dynamodbstreams.md](./services/dynamodbstreams.md) | 4   | Minimal / targeted support    |
| Firehose         | [firehose.md](./services/firehose.md)               | 9   | Minimal / targeted support    |
| Glue             | [glue.md](./services/glue.md)                       | 11  | Minimal / targeted support    |
| OpenSearch       | [opensearch.md](./services/opensearch.md)           | 8   | Minimal / targeted support    |
| AppRegistry      | [appregistry.md](./services/appregistry.md)         | 22  | IaC/discovery-oriented stub   |
| Backup           | [backup.md](./services/backup.md)                   | 18  | IaC/discovery-oriented stub   |
| CloudTrail       | [cloudtrail.md](./services/cloudtrail.md)           | 12  | IaC/discovery-oriented stub   |
| EKS              | [eks.md](./services/eks.md)                         | 50  | IaC/discovery-oriented stub   |
| ELBv2            | [elb.md](./services/elb.md)                         | 22  | IaC/discovery-oriented stub   |
| MSK              | [msk.md](./services/msk.md)                         | 30  | IaC/discovery-oriented stub   |
| Organizations    | [organizations.md](./services/organizations.md)     | 15  | IaC/discovery-oriented stub   |
| Transfer Family  | [transfer.md](./services/transfer.md)               | 13  | IaC/discovery-oriented stub   |

<!-- END overcast:service-index -->

Want to add support for a new AWS service? See
[CONTRIBUTING.md § How to add a service](https://github.com/overcast-sh/overcast/blob/main/CONTRIBUTING.md#how-to-add-a-service)
in the repository.

---

## Event pipelines

| Pipeline                          | Status       |
| --------------------------------- | ------------ |
| SNS → SQS subscription            | ✅ Supported |
| SQS → Lambda event source mapping | ✅ Supported |
| DynamoDB Streams → SQS (Pipes)    | ✅ Supported |
| DynamoDB Streams → Lambda (ESM)   | ✅ Supported |

---

## Web management console

The full image (`ghcr.io/overcast-sh/overcast`) serves a web management console
on **http://localhost:4567** — in-process, from the same binary, so there is no
second server to start and the AWS API on 4566 never depends on it. Set
`OVERCAST_UI_PORT` (or `--ui-port`) to move it; `0` disables it. A tour with
screenshots is at [overcast.sh/console](https://overcast.sh/console/).

| Feature | What it does |
| --- | --- |
| Dashboard | Service cards with live status and emulation tier |
| Per-service UI | S3 browser, SQS message inspector, DynamoDB item editor, Lambda test/invoke, and more |
| Live activity feed | Every API call as it happens — operation, resource, status code, latency |
| Request traces | Full bodies, headers, log lines and internal hops for any call, searchable. Needs `OVERCAST_DEBUG=true` |
| Inbox | Everything Overcast would have sent: SES mail, SNS email subscriptions and Cognito verification mail via the built-in SMTP server on `:1025`, plus SNS SMS and HTTP webhook deliveries — searchable, headers and bodies included, with no third-party mail catcher |
| Topology map | Cross-service resource relationships |
| Real-time updates | Server-sent events push changes as they happen |

If the console stops responding to clicks while many Lambdas run or transfers are
in flight, you are hitting the browser's 6-connection HTTP/1.1 limit — the live
feed and progress streams hold the sockets. Serving the console over HTTPS
unlocks HTTP/2 and fixes it: see [HTTPS and HTTP/2](./https.md).

> [!TIP]
> Request traces are the console's strongest debugging tool and the one thing it
> cannot show by default. Start the daemon with `OVERCAST_DEBUG=true` and every
> call gets a full trace — see [Debug endpoints](./debug-endpoints.md).
