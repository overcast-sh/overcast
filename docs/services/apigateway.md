---
title: "API Gateway — Amazon API Gateway"
description: "Quick start, invoke URLs for REST v1 and HTTP v2, the integrations and authorizers that execute, and how usage plan throttling and quotas are measured."
section: "Service Reference"
tags:
  - amazon
  - api
  - apigateway
  - docs
  - gateway
  - services
---

# API Gateway — Amazon API Gateway

REST (v1) and HTTP (v2) APIs are deployed and invoked for real: a request
routes through the matching method and integration to Lambda, an HTTP backend,
or a mock response.

**Status:** ✅ Supported

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
API=$(aws apigateway create-rest-api --name demo --query id --output text)
ROOT=$(aws apigateway get-rest-api --rest-api-id "$API" --query rootResourceId --output text)

aws apigateway put-method --rest-api-id "$API" --resource-id "$ROOT" \
  --http-method GET --authorization-type NONE
aws apigateway put-integration --rest-api-id "$API" --resource-id "$ROOT" \
  --http-method GET --type MOCK
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$ROOT" \
  --http-method GET --status-code 200 \
  --response-templates '{"application/json":"{\"ok\":true}"}'
aws apigateway create-deployment --rest-api-id "$API" --stage-name dev

curl "http://localhost:4566/restapis/$API/dev/_user_request_/"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## Invoke URLs

| API | Path-style | Host-routed |
| --- | --- | --- |
| REST v1 | `{base}/restapis/{apiId}/{stage}/_user_request_/{path}` | `http://{apiId}.execute-api.{region}.{base}/{stage}/{path}` |
| HTTP v2 | `{base}/v2/apis/{apiId}/stages/{stage}/{path}` | `http://{apiId}.execute-api.{region}.{base}/{stage}/{path}` |

Host-routed is the shape real AWS uses; `{base}` is whatever hostname you
reached Overcast on. Set `OVERCAST_HOSTNAME=localhost.overcast.sh` so it
resolves for every caller — see
[Hostnames that resolve for every caller](../networking/hostnames.md).

HTTP v2 reports the host-routed form in `apiEndpoint`, minted on that hostname
rather than `amazonaws.com`, so `Fn::GetAtt ApiEndpoint` returns a URL you can
dial. REST v1 has no such field, matching AWS — the console composes it
client-side, falling back to path-style only when the endpoint it is connected
to cannot carry a subdomain. Stack outputs that compose an invoke URL in the
template, as CDK does, are re-hosted onto a reachable origin by
`DescribeStacks`; see [what host and port a URL carries](../networking/urls.md).

## What works

| Area | Behaviour |
| --- | --- |
| REST v1 integrations | `AWS_PROXY`, `AWS` (non-proxy Lambda), `HTTP_PROXY`, `HTTP` and `MOCK` all execute |
| HTTP v2 integrations | `AWS_PROXY` and `HTTP_PROXY` execute |
| Authorizers | `COGNITO_USER_POOLS` (v1) and `JWT` (v2) are verified: RS256 signature, expiry, issuer and audience. `TOKEN`/`REQUEST` (v1) and `REQUEST` (v2) invoke the configured Lambda function and evaluate its response — see below |
| Usage plans | Measured on every request — see below |
| Stages and deployments | Deployments, stages, stage variables, and per-stage routing on both invoke forms |
| Keys | API keys, usage plans, usage plan keys, and `GetUsage`'s daily `[used, remaining]` log |

## Differences from AWS

| Area                       | On AWS                                     | Overcast                                                                                                                                      |
| -------------------------- | ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Mapping templates          | Evaluated as VTL                           | Not evaluated — a `MOCK` integration returns its integration response's `application/json` template verbatim; other values pass through as-is |
| IAM authorizers            | Enforced on every request                  | `AWS_IAM` methods/routes are stored but not enforced at request time — the SigV4 signature they require isn't verified                        |
| Request validation         | A request validator rejects a bad request  | Request validators are stored but not enforced                                                                                                |
| WebSocket APIs             | Execution and connection management        | `WEBSOCKET` is accepted on creation; execution is not implemented, and the connection-management route returns `501`                          |
| Usage counters             | A quota is carried across the whole period | In memory, so a restart resets them                                                                                                           |
| Usage plans on HTTP APIs   | v2 has no API-key or usage-plan concept    | Nothing is measured either                                                                                                                    |
| `GetUsage` range           | No documented cap                          | A range wider than 400 days is refused with `BadRequestException`                                                                             |

## Usage plan throttling and quotas

A method with `apiKeyRequired: true` resolves the caller's `x-api-key` to an API
key and to the usage plan covering `{restApiId, stage}`; a request with no such
plan is refused with `403`. The plan's limits are then measured on every
request.

| Limit | Model |
| --- | --- |
| Throttle | `throttle.rateLimit` tokens per second refilling a bucket of `throttle.burstLimit`, per (usage plan, API key) |
| Quota | `quota.limit` requests per `quota.period` (`DAY`, `WEEK`, `MONTH`) in calendar-aligned UTC windows; `quota.offset` is subtracted from the limit in the first period only |

Reaching a limit logs a warning and publishes an `apigateway:Throttled` event —
visible on the console's Events and Usage Plans pages — coalesced to at most one
per key per second.

**Rejection is opt-in and off by default.** An over-limit request is counted,
reported, and served normally. Set `OVERCAST_ENFORCE_APIGATEWAY_THROTTLE=true`
to reject it the way AWS does, bearing in mind that a working local stack then
starts getting `429`s:

| Condition | Status | `x-amzn-ErrorType` | Body |
| --- | --- | --- | --- |
| Rate or burst exceeded | `429` | `TooManyRequestsException` | `{"message":"Too Many Requests"}` |
| Quota exhausted | `429` | `LimitExceededException` | `{"message":"Limit Exceeded"}` |

A rejected request consumes neither quota nor a token, matching AWS. A plan
configuring neither a throttle nor a quota never rejects anything, whatever the
flag says.

## Lambda authorizers

A `TOKEN` or `REQUEST` authorizer (REST v1) or a `REQUEST` authorizer (HTTP
v2) invokes the configured function and evaluates the IAM policy it returns
against the request's `execute-api:Invoke` ARN — `Deny`, or no statement
matching at all, answers `403`
`{"Message":"User is not authorized to access this resource with an explicit
deny"}`; a function that throws exactly `Unauthorized` answers `401`
`{"message":"Unauthorized"}`; any other invocation failure answers `500`. An
HTTP v2 authorizer using payload format `2.0` with `enableSimpleResponses` may
return `{isAuthorized, context}` instead of a policy. `authorizerResultTtlInSeconds`
caches the decision per identity source value, shared across every
route/method that uses the authorizer, matching AWS.

## Gotchas

> [!WARNING]
> An unenforced authorizer is an open endpoint. A route protected only by an
> `AWS_IAM` authorizer is reachable without credentials here, so an
> authorization test that passes locally proves nothing for that one case.

<!-- BEGIN overcast:capabilities -->

## Operations

102 of 104 listed operations are implemented.
Per-operation status, notes and AWS API links: [API Gateway operations](apigateway/operations.md).

<!-- END overcast:capabilities -->

## Related

- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [Networking and host-based addressing](../networking.md)
- [AWS API reference](https://docs.aws.amazon.com/apigateway/latest/api/Welcome.html)
