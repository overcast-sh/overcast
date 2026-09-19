---
title: "Configuration"
description: "Overcast is configured entirely with environment variables. The handful most people set, where each area is documented, and the service names OVERCAST_STATE_<SERVICE> is keyed by."
section: "Reference"
tags:
  - configuration
  - docs
  - environment
  - settings
---

# Configuration

Every setting is an environment variable; there is no config file. Set them on
the command line, with `docker run -e`, or in a Compose `environment:` block:

```bash
OVERCAST_PORT=4566 OVERCAST_LOG_LEVEL=debug overcast serve
```

## The ones most people set

| Variable            | Default            | Change it when                                                                    |
| ------------------- | ------------------ | ---------------------------------------------------------------------------------- |
| `OVERCAST_PORT`     | `4566`             | Something else already has 4566                                                    |
| `OVERCAST_HOSTNAME` | `localhost`        | Client-facing URLs need a name containers share too — usually `localhost.overcast.sh` |
| `OVERCAST_STATE`    | `auto`             | You want `memory` for disposable tests, or `persistent` to survive a restart       |
| `OVERCAST_DATA_DIR` | `~/.overcast/data` | Persisted state belongs somewhere else                                             |
| `OVERCAST_LOG_LEVEL`| `info`             | You are filing a bug — use `debug`                                                 |
| `OVERCAST_DEBUG`    | `false`            | You want the `/_overcast/debug/*` endpoints                                        |

Everything else is in the [environment variable
reference](./configuration/reference.md).

## By area

| Page                                                                              | Answers                                                       |
| --------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| [Environment variable reference](./configuration/reference.md)                    | Every variable Overcast reads, and its default                |
| [Bind address and port](./configuration/ports.md)                                 | Which addresses and ports the API and console listen on       |
| [Running two instances on one host](./configuration/two-instances.md)             | Which ports collide, and which move out of the way            |
| [Log levels](./configuration/log-levels.md)                                       | What each `OVERCAST_LOG_LEVEL` prints                         |
| [Exposing MCP](./configuration/mcp.md)                                            | Reaching `/_overcast/mcp` from a non-local client             |
| [Storage and persistence](./storage.md)                                           | What `OVERCAST_STATE=auto` picks, and per-service overrides   |
| [Networking](./networking.md)                                                     | Hostnames, Docker networks and VPC egress                     |
| [HTTPS and HTTP/2](./https.md)                                                    | `OVERCAST_TLS` and getting the local CA trusted               |
| [LocalStack environment variables](./migration/environment-variables.md)          | Which LocalStack variables are read as aliases                |

## Service names

The per-service storage override `OVERCAST_STATE_<SERVICE>` is keyed by the
names below, upper-cased. CloudWatch Logs is `logs`, so its override is
`OVERCAST_STATE_LOGS`; `OVERCAST_STATE_CLOUDWATCH_LOGS` names nothing and is
rejected at startup.

Every service listed always runs — there is nothing to switch off, and nothing
to configure to get one. Each name is the service's AWS CLI name, which for
several services matches neither the display name nor the `aws-cdk-lib` module
you would import, so the CDK column carries that mapping too. For per-service
endpoint coverage, follow the doc links in [Documentation §
Services](./README.md#services).

<!-- BEGIN overcast:service-names -->

| Name              | Service          | CDK module (`aws-cdk-lib/…`)                       |
| ----------------- | ---------------- | -------------------------------------------------- |
| `s3`              | S3               | `aws-s3`                                           |
| `sqs`             | SQS              | `aws-sqs`                                          |
| `dynamodb`        | DynamoDB         | `aws-dynamodb`                                     |
| `lambda`          | Lambda           | `aws-lambda`                                       |
| `apigateway`      | API Gateway      | `aws-apigateway`, `aws-apigatewayv2`               |
| `appsync`         | AppSync          | `aws-appsync`                                      |
| `cloudfront`      | CloudFront       | `aws-cloudfront`, `aws-cloudfront-origins`         |
| `cognito`         | Cognito          | `aws-cognito`                                      |
| `ec2`             | EC2 / VPC        | `aws-ec2`                                          |
| `sns`             | SNS              | `aws-sns`                                          |
| `stepfunctions`   | Step Functions   | `aws-stepfunctions`, `aws-stepfunctions-tasks`     |
| `iam`             | IAM              | `aws-iam`                                          |
| `ecs`             | ECS              | `aws-ecs`                                          |
| `ecr`             | ECR              | `aws-ecr`, `aws-ecr-assets`                        |
| `kms`             | KMS              | `aws-kms`                                          |
| `kinesis`         | Kinesis          | `aws-kinesis`                                      |
| `eventbridge`     | EventBridge      | `aws-events`, `aws-events-targets`                 |
| `scheduler`       | Scheduler        | `aws-scheduler`                                    |
| `cloudformation`  | CloudFormation   | `aws-cloudformation`                               |
| `rds`             | RDS              | `aws-rds`                                          |
| `elasticache`     | ElastiCache      | `aws-elasticache`                                  |
| `efs`             | EFS              | `aws-efs`                                          |
| `appconfig`       | AppConfig        | `aws-appconfig`                                    |
| `appconfigdata`   | AppConfigData    | — (runtime data plane; no constructs)              |
| `secretsmanager`  | Secrets Manager  | `aws-secretsmanager`                               |
| `ssm`             | SSM              | `aws-ssm`                                          |
| `logs`            | CloudWatch Logs  | `aws-logs`                                         |
| `ses`             | SES              | `aws-ses`                                          |
| `sts`             | STS              | — (used by the CDK CLI itself)                     |
| `route53`         | Route 53         | `aws-route53`, `aws-route53-targets`               |
| `autoscaling`     | Auto Scaling     | `aws-autoscaling`, `aws-applicationautoscaling`    |
| `pipes`           | Pipes            | `aws-pipes`                                        |
| `waf`             | WAF v2           | `aws-wafv2`                                        |
| `shield`          | Shield           | `aws-shield`                                       |
| `acm`             | ACM              | `aws-certificatemanager`                           |
| `athena`          | Athena           | `aws-athena`                                       |
| `bedrock`         | Bedrock          | `aws-bedrock`                                      |
| `cloudwatch`      | CloudWatch       | `aws-cloudwatch`, `aws-cloudwatch-actions`         |
| `dynamodbstreams` | DynamoDB Streams | — (enabled by the `stream` prop on `aws-dynamodb`) |
| `firehose`        | Firehose         | `aws-kinesisfirehose`                              |
| `glue`            | Glue             | `aws-glue`                                         |
| `opensearch`      | OpenSearch       | `aws-opensearchservice`                            |
| `appregistry`     | AppRegistry      | `aws-servicecatalogappregistry`                    |
| `backup`          | Backup           | `aws-backup`                                       |
| `cloudtrail`      | CloudTrail       | `aws-cloudtrail`                                   |
| `eks`             | EKS              | `aws-eks`                                          |
| `elbv2`           | ELBv2            | `aws-elasticloadbalancingv2`                       |
| `msk`             | MSK              | `aws-msk`                                          |
| `organizations`   | Organizations    | — (no constructs)                                  |
| `transfer`        | Transfer Family  | `aws-transfer`                                     |

<!-- END overcast:service-names -->

## Related

- [Environment variable reference](./configuration/reference.md) — every variable, with its default
- [Bind address and port](./configuration/ports.md)
- [Log levels](./configuration/log-levels.md)
- [Running two instances on one host](./configuration/two-instances.md)
- [MCP endpoint](./configuration/mcp.md)
- [CLI reference](./cli.md) — the flags that mirror these variables
