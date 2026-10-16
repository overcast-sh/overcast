---
title: "Lambda Web Functions — AWS Lambda Web Functions"
description: "Lambda Web Functions — web functions, immutable revisions and endpoints, with resource policies and tags. The control plane is complete; nothing serves an endpoint yet."
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
them. The control plane is complete; an endpoint's domain name is allocated but
no HTTP server runs behind it yet.

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
| Account settings | AWS's documented default quotas and the Region's function count |
| CloudFormation | `AWS::Lambda::WebFunction`, `WebFunctionRevision` and `WebFunctionEndpoint` |
| System map | A node per web function, listing its endpoints and the revision each serves |

## Differences from AWS

| Area | On AWS | Overcast |
| --- | --- | --- |
| Running the code | Lambda fetches the package from S3 and starts your server | Nothing is fetched or started; a revision is Active as soon as it exists |
| Endpoint domain | Serves HTTPS | Allocated and returned, not served |
| `IamAuth` and resource policies | Evaluated on every request to the endpoint | Stored; no request reaches an endpoint |
| Create responses | `Pending`, then `Active` after a delay | `Pending` in the create response, `Active` on every read after |

The full list is on [Lambda Web Functions limitations](./lambda-web/limitations.md).

## Gotchas

> [!WARNING]
> Web Functions are not Lambda functions. `aws lambda list-functions` does not
> show them, and `aws lambda invoke` cannot call them. They are a separate
> service, `lambda-web`, whose paths all start with `/2025-03-07/`.

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
