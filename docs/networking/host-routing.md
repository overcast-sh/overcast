---
title: "Host-routed addressing"
description: "Which AWS subdomain forms Overcast routes, how it decides whether a Host belongs to S3 or to a service, and the reserved labels a bucket name cannot use."
section: "Networking"
tags:
  - docs
  - dns
  - networking
  - routing
  - s3
---

# Host-routed addressing

Overcast accepts both AWS addressing styles and rewrites every host-routed
request onto the same handlers path-style requests use, so authorizers, stage
variables, integration dispatch and event publishing behave identically either
way.

| Style | Looks like | Use it |
| --- | --- | --- |
| Path-style | `http://localhost:4566/restapis/{apiId}/{stage}/_user_request_/…` | Always available, no configuration. What an SDK falls back to |
| Host-routed | `{apiId}.execute-api.{region}.{base}/{stage}/…` | When your client produces it, or when the feature has no path-style form |

The subdomain has to resolve to wherever Overcast is listening — see
[Hostnames that resolve for every caller](./hostnames.md).

## What works today

| Service | Host pattern | Notes |
| ------------------------ | ------------------------------------------------ | ---------------------------------------------------------------------- |
| API Gateway (REST v1) | `{apiId}.execute-api.{region}.{base}/{stage}/...` | Stage is always the first path segment, same as real AWS. |
| API Gateway (HTTP v2) | `{apiId}.execute-api.{region}.{base}/...` | `$default` stage: no stage segment. Named stages: `{stage}/...` prefix, resolved against your API's actual stages. |
| Lambda function URLs | `{urlId}.lambda-url.{region}.{base}/...` | No path-style equivalent — the only way to invoke a function URL, here and on AWS. See [Lambda function URLs](#lambda-function-urls). |
| AppSync (GraphQL) | `{apiId}.appsync-api.{region}.{base}/graphql` | Also reachable at `/realtime` on the same host — Overcast colocates the GraphQL and realtime endpoints. |
| AppSync (subscriptions) | `{apiId}.appsync-realtime-api.{region}.{base}` | The host real AWS serves subscriptions on, and the one Amplify derives by substituting into the GraphQL URL. Routes to the same endpoint as `/realtime`. |
| CloudFront | `{distributionId}.cloudfront.{base}` | Global, so there is no region segment. `DomainName` is minted on the hostname you reached Overcast on rather than the literal `cloudfront.net`. |
| Elastic Load Balancing | `{name}-{id}.{region}.elb.{base}/...` | AWS's own load balancer shape, so the region sits *before* the label. Reaches the target group the matching listener forwards to — see [ELB](../services/elb.md). |
| S3 (virtual-hosted style) | `{bucket}.s3[.{region}].{base}/...` or `{bucket}.{base}/...` | Both forms work. The second is what an AWS SDK emits against a custom endpoint with path-style disabled, and the only form CDK's asset publisher uses. See [sdk-cli.md](../sdk-cli.md#s3-addressing-styles) and [CDK troubleshooting](../cdk/troubleshooting.md#s3-asset-upload-fails-on-windows). |

**The Host is case-insensitive in every part** — resource ID, service label,
region segment and base domain alike — because a hostname is (RFC 4343). So
`E1PQRS2T3U4V5W.cloudfront.localhost.overcast.sh:4566` and the all-lowercase
form a browser sends reach the same distribution, and `MyBucket.localhost:4566`
reaches bucket `mybucket`. **Paths stay case-sensitive**, as on AWS:
`/_overcast/cloudfront/distributions/{distributionId}/...` and every other
path-style route must match exactly.

**Every one of these works over HTTPS.** A TLS wildcard matches exactly one
label, so a name with a variable middle (`{apiId}.execute-api.{region}.{base}`)
can never be covered by a fixed certificate; with `OVERCAST_TLS=auto` the local
CA mints one for it during the handshake instead — see
[How the local CA works](../https/how-it-works.md#which-names-the-certificate-covers).

## How Overcast decides who owns a Host

S3 virtual-hosted addressing and the services above share one hostname space,
so one rule resolves them, applied in order:

1. **`{bucket}.s3.{...}` / `{bucket}.s3-{region}.{...}` → S3.** The bucket is
   everything before the first `.s3.`, so bucket names containing dots stay
   addressable here.
2. **`{bucket}.{base}` → S3**, where `{base}` is `localhost`,
   `localhost.overcast.sh`, `localhost.localstack.cloud`, `localhost.floci.io`,
   or your `OVERCAST_HOSTNAME`. Exception: if the part in front of the base
   carries a service label (`execute-api`, `lambda-url`, `appsync-api`,
   `appsync-realtime-api`, `cloudfront`, `elb`) as its second or later
   dot-segment, it is a service address, and rule 3 takes it.
3. **`{id}.{label}[.{region}].{base}` → the owning service**, for the labels in
   the table above. `elb` is the one that carries its region in front of the
   label instead (`{name}-{id}.{region}.elb.{base}`), matching AWS.
4. **Anything else stays path-style** and reaches S3, the emulator's catch-all.

The order is fixed here rather than configurable, so the same Host always
resolves the same way.

> [!IMPORTANT]
> **Reserved service labels.** A bucket whose **second or later** dot-segment is
> `execute-api`, `lambda-url`, `appsync-api`, `appsync-realtime-api`,
> `cloudfront` or `elb` cannot be addressed by rule 2 — `my.execute-api.localhost`
> is an API Gateway invoke. Use path-style (`localhost:4566/my.execute-api/key`)
> or the explicit form (`my.execute-api.s3.localhost`); both work.
>
> A bucket named *exactly* like a label is unaffected: `execute-api.localhost` is
> the bucket `execute-api`, because a host-routed address always carries a
> non-empty resource ID in front of the label.
>
> Overcast warns at `CreateBucket` when a name carries a reserved label, naming
> both escapes. The bucket is still created — AWS accepts the name, so refusing
> it would fail a stack locally that deploys fine against AWS.

## Known AWS resource subdomains

AWS publishes no single list of the hostnames that carry a resource ID: the SDK
endpoint rulesets and Smithy's `endpointPrefix` cover control-plane endpoints
only, and `execute-api`, `lambda-url` and `appsync-api` appear in none of them.
This table is the centralised list, maintained by hand.

| Form | Overcast | Notes |
| --- | --- | --- |
| `{apiId}.execute-api.{region}.{base}` | ✅ routed | API Gateway REST v1 and HTTP v2 invoke |
| `{urlId}.lambda-url.{region}.{base}` | ✅ routed | Lambda function URLs |
| `{apiId}.appsync-api.{region}.{base}` | ✅ routed | AppSync GraphQL |
| `{apiId}.appsync-realtime-api.{region}.{base}` | ✅ routed | AppSync subscriptions. Amplify derives this host from the GraphQL URL, so it must route even though Overcast serves both endpoints from one place |
| `{distributionId}.cloudfront.net` | ✅ routed | CloudFront distribution. Global, so there is no region segment |
| `{name}-{id}.{region}.elb.amazonaws.com` | ✅ routed | Application Load Balancer. The one shape whose region precedes the label |
| `{bucket}.s3[.{region}].{base}` | ✅ routed | S3 virtual-hosted style |
| `{bucket}.s3.dualstack.{region}.{base}` | ✅ routed | Matched by the `.s3.` rule |
| `{bucket}.s3-{region}.{base}` | ✅ routed | Legacy dash dialect, pre-2019 regions |
| `{bucket}.s3-accelerate.{base}` | ✅ routed | Transfer Acceleration. Routing only — no acceleration is emulated, and AWS forbids periods in bucket names used with it |
| `{bucket}.s3-website[-.]{region}.{base}` | ⚠️ routed | Reaches S3, but static website hosting behaviour is not implemented |
| `{ap}-{account}.s3-accesspoint.{region}.{base}` | ⚠️ routed | Reaches S3 treating the access point alias as a bucket name; access points are not modelled |
| `{bucket}.{base}` | ✅ routed | Bare form. No real-AWS equivalent, but what an SDK emits against a custom endpoint with path-style disabled, and the only form CDK's asset publisher uses |
| `{domain}.auth.{region}.amazoncognito.com` | ❌ | Cognito hosted UI. Use the path-style endpoint |
| `{id}.{hash}.{region}.rds.amazonaws.com` | ❌ | Engine data-plane endpoint |
| `{cluster}.{hash}.{region}.cache.amazonaws.com` | ❌ | Engine data-plane endpoint |
| `b-{n}.{cluster}.{hash}.kafka.{region}.amazonaws.com` | ❌ | Engine data-plane endpoint |
| `{domain}.{region}.es.amazonaws.com` | ❌ | Engine data-plane endpoint |

The engine data-plane endpoints are absent deliberately. Overcast emulates those
services' control planes, so no engine sits behind the hostname to route to, and
every one of those labels (`rds`, `cache`, `kafka`, `es`, `auth`) is a bare
common word: registering one would make a bucket named `my.cache` unaddressable
in the bare form for no benefit. The names Overcast does mint for engine
containers are on [Data-plane endpoints](./data-plane-endpoints.md).

`{base}` is whatever hostname the request arrived on — no domain is hardcoded.
Point requests at `localhost`, a wildcard domain, or a Docker service name; the
response echoes back the base you called in.

## Lambda function URLs

`CreateFunctionUrlConfig`, `GetFunctionUrlConfig`, `UpdateFunctionUrlConfig`,
`DeleteFunctionUrlConfig` and `ListFunctionUrlConfigs` are implemented under the
real API paths (`/2021-10-31/functions/{name}/url[s]`). The returned
`FunctionUrl` uses the Host you called Overcast on:

```
http://<url-id>.lambda-url.<region>.<host>:<port>/
```

Overcast is a development tool rather than a security boundary, so four fields
are simplified:

| Field | Behaviour |
| --- | --- |
| `AuthType` (`NONE` / `AWS_IAM`) | Stored and returned, never enforced. Every host-routed invocation runs as if it were `NONE` |
| `Cors` | Stored, returned, **and applied** — `Access-Control-Allow-*` headers are reflected onto invoke responses, which is what browser-based testing needs |
| `InvokeMode: RESPONSE_STREAM` | Accepted, always behaves as `BUFFERED`. There is no streaming function-URL invocation path |
| `Qualifier` | Stored for API-shape correctness, not enforced against invocation — Overcast's Lambda emulator treats aliases and versions as metadata rather than separate executable snapshots |

## Related

- [Hostnames that resolve for every caller](./hostnames.md) — making the subdomain resolve
- [What host and port a URL carries](./urls.md) — what Overcast puts in the URLs it hands back
- [Using AWS SDKs and CLI](../sdk-cli.md) — endpoint configuration per SDK
- [Networking and host-based addressing](../networking.md) — the rest of the addressing story
