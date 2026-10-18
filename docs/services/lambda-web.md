---
title: "Lambda Web Functions — AWS Lambda Web Functions"
description: "Lambda Web Functions — web functions, immutable revisions and endpoints, with resource policies and tags. Revisions run as Node.js servers in Docker containers and endpoints serve HTTP, scaling to zero when idle."
section: "Service Reference"
tags:
  - docs
  - lambda
  - lambda-web
  - serverless
  - services
---

# Lambda Web Functions — AWS Lambda Web Functions

Web functions with their revisions and endpoints, as the `lambda-web` API serves
them. An endpoint's domain name serves HTTP: Overcast starts the revision's
Node.js server in a Docker container on the first request, reverse-proxies to
it, runs more containers as traffic grows, and stops them when idle.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws lambda-web create-web-function --function-name my-web-app \
  --revision-config '{
    "buildConfig": {
      "codeConfig": {"s3Object": {"bucket": "code-bucket", "key": "app.zip"}},
      "runtimeConfig": {"runtime": "nodejs24.x"}},
    "serviceConfig": {"executionRoleArn": "arn:aws:iam::000000000000:role/web-role"}}' \
  --endpoint-config '{
    "endpointName": "dev", "endpointType": "HomeRegion",
    "authType": "ApplicationManaged", "autoDeploymentMode": "LatestRevision"}'

aws lambda-web get-web-function-endpoint --function-name my-web-app --endpoint-name dev
```

The endpoint's `domainName` is a host under Overcast. Send a request to it with
that name as the `Host` header (or as the URL, where `*.localhost` resolves to
loopback):

```bash
curl -H "Host: {domainName from the call above}" http://localhost:4566/
```

The first request starts a container (about half a second); later ones are
answered in a couple of milliseconds. The package must be a zip of a Node.js
server in a versioned S3 bucket that listens on the port in
`AWS_LAMBDA_HTTP_ENDPOINT`.

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials). The `aws lambda-web`
commands need an AWS CLI release that includes them.

## What works

| Area | Behaviour |
| --- | --- |
| Web functions | Create (optionally with a first revision and endpoint), get, list, delete; names or ARNs |
| Revisions | Immutable; `timeoutSeconds` 30 and `maxConcurrencyPerEnvironment` 64 are returned as defaults; the oldest unrouted revisions are removed past 50 |
| Endpoints | `HomeRegion`, `MultiRegion` and `PerRegion`; domain names in AWS's shapes; at most 10 per function |
| Traffic routing | One or two revisions by weight; `LatestRevision` follows the newest revision; a revision an endpoint serves cannot be deleted |
| Updates | Omitted members are kept; an empty `scalingConfig` or `throttleConfig` clears |
| Resource policy | Public policies are rejected; `RevisionId` guards concurrent changes |
| Tags | On the web function; revisions and endpoints inherit them |
| Serving | Host-routed requests on `{id}.{shard}.lambda-web.{region}.{host}`; the `lambda-web-request-id`, `lambda-web-logstream` and `lambda-web-error-type` headers; AWS's platform, runtime and application error types |
| Scaling | Containers start on demand, up to `maxConcurrencyPerEnvironment` requests each, `scalingConfig.maxEnvironments` per endpoint and `LAMBDA_WEB_MAX_ENVIRONMENTS` per host; idle ones stop after `LAMBDA_WEB_IDLE_TTL` |
| Throttling | `throttleConfig.rateLimit` per endpoint and AWS's account-level limit, refused with 429 |
| `IamAuth` | SigV4 and SigV4a checks; the caller's IAM policy and the function's resource policy for `lambda:InvokeWebFunctionEndpoint`; CloudFront distributions with a `lambda` origin access control |
| Telemetry | Application and `platform.*` logs in CloudWatch Logs; `AWS/Lambda/Web` metrics; `lambda-web:*` EventBridge events |
| Account settings | AWS's documented default quotas and the Region's function count |
| CloudFormation | `AWS::Lambda::WebFunction`, `WebFunctionRevision` and `WebFunctionEndpoint` |
| System map | A node per web function, listing its endpoints and the revision each serves |

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| Running the code | Firecracker microVMs, 2 GiB each | Docker containers of `public.ecr.aws/lambda/nodejs:24`, 2 GiB and 2 vCPU each. Needs a Docker daemon; without one a request answers with a 5xx platform error and nothing starts |
| Endpoint domain | Serves HTTPS | Serves HTTP, on the host the caller used (so `localhost:4566` stays out of the stored name) |
| Other Regions | `MultiRegion` and `PerRegion` endpoints replicate | Every Region of the endpoint is served from this process |
| Create responses | `Pending`, then `Active` after a delay | `Pending` in the create response, `Active` on every read after |

The full list is on [Lambda Web Functions limitations](./lambda-web/limitations.md).

## Gotchas

> [!WARNING]
> Web Functions are not Lambda functions. `aws lambda list-functions` does not
> show them, and `aws lambda invoke` cannot call them. They are a separate
> service, `lambda-web`, whose paths all start with `/2025-03-07/`.

Overcast's `RealIP` middleware sets the connection's remote address from
`X-Forwarded-For`, so an application sees its client's address twice in that
header.

<!-- BEGIN overcast:capabilities -->

## Operations

All 20 listed operations are implemented.
Per-operation status, notes and AWS API links: [Lambda Web Functions operations](lambda-web/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Lambda Web Functions limitations](./lambda-web/limitations.md)
- [Lambda](./lambda.md) — event functions, which really run
- [S3](./s3.md) — where a revision's package is uploaded
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS Lambda Web Functions](https://docs.aws.amazon.com/lambda/latest/dg/web-functions.html)
