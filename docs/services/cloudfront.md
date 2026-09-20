---
title: "CloudFront — Amazon CloudFront"
description: "Quick start, the three ways to reach a distribution, what the origin proxy caches and executes, and the security features that are stored but never enforced."
section: "Service Reference"
tags:
  - amazon
  - cloudfront
  - docs
  - services
---

# CloudFront — Amazon CloudFront

A request through a distribution is matched against the cache behaviours,
proxied to the origin, cached, and passed through any CloudFront Function
attached to it.

**Status:** ✅ Supported

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
aws s3 mb s3://site && echo 'hello from the edge' > index.html
aws s3 cp index.html s3://site/

ID=$(aws cloudfront create-distribution --distribution-config '{
  "CallerReference":"demo","Comment":"","Enabled":true,
  "DefaultRootObject":"index.html",
  "Origins":{"Quantity":1,"Items":[{"Id":"s3","DomainName":"site.s3.amazonaws.com",
    "S3OriginConfig":{"OriginAccessIdentity":""}}]},
  "DefaultCacheBehavior":{"TargetOriginId":"s3","ViewerProtocolPolicy":"allow-all"}
}' --query Distribution.Id --output text)

curl "http://localhost:4566/_overcast/cloudfront/distributions/$ID/"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

An origin domain Overcast already answers for — `{bucket}.s3.…`, an API Gateway
or Lambda function URL, any subdomain of the host you reached Overcast on — is
dialled locally rather than out to AWS. Everything else is fetched for real.

## Reaching a distribution

| Form | URL |
| --- | --- |
| Path-style | `http://localhost:4566/_overcast/cloudfront/distributions/{ID}/{path}` |
| Host header | `curl -H "Host: {DomainName}" http://localhost:4566/{path}` |
| Resolvable name | `http://{DomainName}/{path}`, with `OVERCAST_HOSTNAME` set |

`DomainName` is minted on the host you reached Overcast on —
`{id}.cloudfront.localhost.overcast.sh:4566`, not `cloudfront.net` — so the
name a stack output hands you is one you can dial. Set
`OVERCAST_HOSTNAME=localhost.overcast.sh` to make it resolve on every OS; see
[Hostnames that resolve for every caller](../networking/hostnames.md).

## What works

| Area | Behaviour |
| --- | --- |
| Distributions, policies, keys | Full CRUD with `If-Match` ETags and `CallerReference` idempotency |
| Origin routing | Cache-behaviour path patterns, `DefaultRootObject`, origin groups with failover, per-origin custom headers |
| Caching | In-process response cache on `GET`, purged by `CreateInvalidation` |
| Invalidations | Path and cache-tag (`#tag`) invalidations; `Status` is `Completed` immediately |
| CloudFront Functions | Viewer-request and viewer-response functions execute for real, including URI rewrites and short-circuit responses |
| Continuous deployment | `SingleWeight` and `SingleHeader` policies actually split traffic to the staging distribution |
| Errors and geo | `CustomErrorResponses`, `ViewerProtocolPolicy`, and geo restriction on `CloudFront-Viewer-Country` |
| Access logs | Written to the configured S3 bucket in W3C format when `Logging.Enabled` |

## Differences from AWS

| Area                                               | On AWS                                                    | Overcast                                                                      |
| -------------------------------------------------- | --------------------------------------------------------- | ----------------------------------------------------------------------------- |
| Deployment                                         | `Status` is `InProgress`, then `Deployed` once propagated | `Status` is `Deployed` on create — no propagation delay                       |
| ETags                                              | A content hash                                            | A quoted version counter (`"1"`, `"2"`)                                       |
| Cache key                                          | Path, query string, headers, cookies and `Vary`           | Path and query string only                                                    |
| Cache TTL                                          | Origin `Cache-Control` within `MinTTL` and `MaxTTL`       | The behaviour's cache policy `DefaultTTL`, else 24 hours; the rest is ignored |
| Signed URLs and cookies                            | Verified against the trusted key groups                   | Not verified; `ActiveTrustedKeyGroups` always reports disabled                |
| Origin access (OAC / OAI)                          | Origin requests are signed                                | Stored, never enforced — origin requests are not signed                       |
| Response headers and origin request policies       | Applied to every request                                  | Stored and returned; the proxy applies neither                                |
| Monitoring, real-time logs, field-level encryption | Full API                                                  | Metadata only                                                                 |

The full list, with what each unenforced feature means for a stack that relies
on it, is in [CloudFront limitations](./cloudfront/limitations.md).

## Gotchas

> [!CAUTION]
> A distribution is not an access boundary here. Trusted key groups, signed
> cookies and origin access control are stored and none are enforced, so an
> "OAC-protected" bucket stays directly readable and a signed URL is never
> checked. Do not use Overcast to test whether private content is private.

The proxy is similarly relaxed about which version of a function it runs.

> [!WARNING]
> A CloudFront Function runs whether or not it has been published. The proxy
> resolves the function by ARN and never checks its stage, so a `DEVELOPMENT`
> version attached to a behaviour executes on live requests.

<!-- BEGIN overcast:capabilities -->

## Operations

All 88 listed operations are implemented.
Per-operation status, notes and AWS API links: [CloudFront operations](cloudfront/operations.md).

<!-- END overcast:capabilities -->

## Related

- [CloudFront limitations](./cloudfront/limitations.md) — the full divergence list
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/cloudfront/latest/APIReference/Welcome.html)
