---
title: "Lambda — AWS Lambda"
description: "Quick start, the execution model and warm-environment reuse, provisioned concurrency, event source mappings, extensions and logging, and how function code reaches Overcast."
section: "Service Reference"
tags:
  - docs
  - lambda
  - serverless
  - services
---

# Lambda — AWS Lambda

Functions really run: `Invoke` starts a container from the official
`public.ecr.aws/lambda/*` base image and talks to it over the Lambda Runtime API.

**Status:** ✅ Supported

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

echo 'exports.handler = async (e) => ({ ok: true, got: e });' > index.js
zip -q fn.zip index.js

aws lambda create-function --function-name hello \
  --runtime nodejs22.x --handler index.handler \
  --role arn:aws:iam::000000000000:role/lambda-role \
  --zip-file fileb://fn.zip

aws lambda invoke --function-name hello --payload '{"hi":1}' \
  --cli-binary-format raw-in-base64-out out.json && cat out.json
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

> [!IMPORTANT]
> Real execution requires Docker. Without it `Invoke` returns a stub response and
> does not run your code. In a container, bind-mount the Docker socket.

## What works

| Area | Behaviour |
| --- | --- |
| Execution | A container per execution environment, from the official base image for the function's runtime, driven over the Runtime API. |
| Environment reuse | Containers are reused for sequential invocations and scaled out one per concurrent invocation. Surplus stays warm until a 15-minute idle sweep. |
| Provisioned concurrency | `PutProvisionedConcurrencyConfig` really pre-initialises environments: held open regardless of the sweep, replenished when one is lost, rebuilt against a new code or config revision. |
| Proactive init | Ten seconds after a function's configuration settles, one environment is created in the background so the next request lands warm. |
| Versions, aliases, function URLs, layers | Full CRUD, including `create-function --publish`, which publishes version 1 in the same call. Layers are expanded into `/opt` before the runtime starts, later layers overriding earlier ones. |
| Event source mappings | SQS, Kinesis and DynamoDB Streams pollers, including `FunctionResponseTypes: ["ReportBatchItemFailures"]`. |
| Async invocation | `MaximumRetryAttempts` (0–2), `MaximumEventAgeInSeconds` (60–21600), on-success and on-failure destinations, and `DeadLetterConfig` — for HTTP `Event` invokes and for S3, EventBridge, Scheduler and SNS alike. |
| Extensions | Executables under `/opt/extensions` start before the runtime, with `register`, `event/next`, the Logs API and the Telemetry API. |
| Logging | `LogFormat`, `ApplicationLogLevel`, `SystemLogLevel` and a custom `LogGroup` are all honoured. |
| Metrics | `AWS/Lambda` `Invocations`, `Errors`, `Duration`, `Throttles` and `ConcurrentExecutions` are recorded for every invocation mechanism. |
| Container images | `PackageType=Image` runs a real image from this account's [ECR](ecr.md), including CDK's `DockerImageFunction`. `ImageConfig` and `update-function-code --image-uri` both apply — see [Examples](lambda/examples.md#container-images). |
| Hot reload | A local source directory bind-mounted read-only at `/var/task`, retiring the warm environment when the tree changes. |
| Function lifecycle | `State` and `LastUpdateStatus` are both reported, so `aws lambda wait function-active`, `wait function-updated` and the SDK, CDK and SAM waiters over them return. |

## Differences from AWS

| Area               | On AWS                             | Overcast                                                                                                                      |
| ------------------ | ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| X-Ray tracing      | Traces recorded                    | `TracingConfig` stored and returned; no segment is ever recorded                                                              |
| `EphemeralStorage` | Size enforced                      | Stored; `/tmp` gets whatever the Docker host gives it                                                                         |
| `KMSKeyArn`        | Encrypted at rest                  | An association only — environment variables are stored in plaintext                                                           |
| Resource policies  | Enforced                           | Stored and validated always; enforced for service-originated invokes only under `OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY`     |
| Concurrency        | Account-wide quotas and RPS limits | Per-function reserved concurrency only. The instance and memory limits protect your machine; they are not AWS's account quota |
| Cold-start latency | Real                               | Not simulated                                                                                                                 |
| SnapStart          | Supported                          | Not emulated; no restore records                                                                                              |
| Managed instances  | Supported                          | Not emulated; `PublishTo`, `CapacityProviderConfig` and `TenancyConfig` return `501`                                          |
| Tagging            | All taggable resources             | Functions and event source mappings only; other taggable resources return `501`                                               |

The full list is in [Limitations](./lambda/limitations.md), one table with a
page behind each row: [concurrency](./lambda/concurrency.md),
[execution environments](./lambda/execution-environments.md),
[event delivery and retries](./lambda/async.md),
[logging](./lambda/logging.md) and [runtimes](./lambda/runtimes.md).

## Gotchas

> [!WARNING]
> The instance limits are sized to your Docker host, not to AWS. An invocation
> that cannot get a container waits, and **if it is still waiting when the
> function's timeout expires it is throttled** with a 429
> `TooManyRequestsException`. If you hit that in normal use, raise
> `LAMBDA_MAX_INSTANCES` rather than treating it as AWS behaviour.

An unqualified `DeleteFunction` is the other one to know about: it leaves
published versions, aliases and the version counter behind, so recreating a
function under the same name inherits them.

A second Overcast on the same host finds the Runtime API's default port `9001`
taken and steps off it to an ephemeral port on its own — every execution
environment is handed its own per-container address, so nothing notices. Pin
`LAMBDA_RUNTIME_API_PORT` and a taken port instead disables the container
runtime: the startup warning names the variable, and `/_overcast/health`
reports the failed listener. See
[Running two instances on one host](../configuration/two-instances.md).

## Reaching Overcast from function code

Lambda containers are siblings of Overcast, not children of it, so `localhost`
inside a function is the function's own container. `AWS_ENDPOINT_URL` is set to
an address the container can reach, which is enough for SDK calls that address
resources by name.

It is **not** enough for SQS: AWS SDKs resolve the SQS endpoint from the
`QueueUrl` rather than from client configuration, and `AWS_ENDPOINT_URL` loses to
it — see [SQS: queue URLs and endpoint resolution](./sqs.md#queue-urls-and-endpoint-resolution).
Three things keep that working:

| | How |
| --- | --- |
| URLs the function requests itself | `CreateQueue`, `GetQueueUrl` and `ListQueues` answer on the origin the function called in on, so they are dialable by definition |
| Loopback URLs in the function's environment | Rewritten at container start: an `http://localhost:<port>` or `http://127.0.0.1:<port>` origin on Overcast's own port is re-pointed at the container-reachable endpoint. Other hosts and ports are left alone |
| Split-horizon hostnames | A wildcard-DNS name resolves to `127.0.0.1` on the host and is remapped to Overcast inside each container — see [Hostnames that resolve for every caller](../networking/hostnames.md) |

Setting `OVERCAST_HOSTNAME=localhost.overcast.sh` gives every service one URL
form valid on both sides of the container boundary; `OVERCAST_SPLIT_HORIZON_HOSTS`
adds further names. If your function builds its own SQS client, passing the
endpoint explicitly is always safe:

```js
new SQSClient({ endpoint: process.env.AWS_ENDPOINT_URL });
```

<!-- BEGIN overcast:capabilities -->

## Operations

61 of 62 listed operations are implemented.
Per-operation status, notes and AWS API links: [Lambda operations](lambda/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Lambda limitations](./lambda/limitations.md) — every divergence, in one table
- [Lambda troubleshooting](./lambda/troubleshooting.md) — throttles, layer errors, extension endpoints
- [Lambda examples](./lambda/examples.md) — hot reload, container images, layers, extensions
- [Lambda concurrency](./lambda/concurrency.md) — the instance and memory limits
- [Lambda execution environments](./lambda/execution-environments.md) — what retires a warm container
- [Lambda event delivery and retries](./lambda/async.md) — retries, destinations, batch failures
- [Lambda logging](./lambda/logging.md) — the JSON record vocabulary and its levels
- [Lambda runtimes](./lambda/runtimes.md) — identifiers, deprecation dates, images
- [CloudWatch Logs](./cloudwatch-logs.md) — where function output lands
- [All service pages](./README.md)
- [Environment variable reference](../configuration/reference.md) — every `LAMBDA_*` variable
- [AWS API reference](https://docs.aws.amazon.com/lambda/latest/api/welcome.html)
