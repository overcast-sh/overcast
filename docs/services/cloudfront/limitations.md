---
title: "CloudFront limitations"
description: "Where Overcast's CloudFront diverges from AWS: what the origin proxy caches and ignores, which features are stored but never enforced, and how distribution state is scoped."
section: "Service Reference"
tags:
  - cloudfront
  - docs
  - limitations
  - services
---

# CloudFront limitations

Every divergence from real CloudFront, grouped by what it costs you. The
working set is on [CloudFront](../cloudfront.md).

## Not enforced

These features are stored, returned by their `Get*` operations, and accepted by
CloudFormation — but nothing acts on them. A stack that depends on one for
correctness will pass locally and fail on AWS, or the reverse.

| Feature | What is missing |
| --- | --- |
| Trusted signers / trusted key groups | No signature verification. `ActiveTrustedSigners` and `ActiveTrustedKeyGroups` report `Enabled: false, Quantity: 0` on every distribution |
| Origin access control and origin access identity | Origin requests are not SigV4-signed; the bucket behind an "OAC-protected" origin is directly readable |
| Response headers policies | Headers in the policy are never added, removed or overridden |
| Origin request policies | The set of headers, cookies and query strings forwarded to the origin is not filtered by the policy |
| Field-level encryption | Configs and profiles are CRUD only; no field is ever encrypted |
| Real-time log configs | Stored; no log stream is produced. `RealtimeLogConfigArn` is never read by the request path |
| Monitoring subscriptions | Stored; no additional metrics are emitted |
| `Compress`, `OriginShield` | Stored and echoed; no compression, no shield tier |

> [!CAUTION]
> The first two rows are the ones that bite: nothing here verifies a signed URL
> or restricts an origin to the distribution. Treat private-content behaviour as
> untested until it runs on AWS.

## Request paths

The proxy keeps the request path percent-encoded, exactly as the viewer sent
it. The origin receives it byte-for-byte, a viewer-request function's
`event.request.uri` holds it, and it is the path in the cache key. A `uri`
that a function returns with raw UTF-8, spaces, or a stray `%` reaches the
origin percent-encoded.

Cache behaviours are matched after the percent-encoding step of AWS's
RFC 3986 normalisation, so `/%7Euser` matches `/~user/*`, but `%40` never
matches `@` and `%2F` is not a separator. Two parts of AWS's normalisation are
not applied:

- Dot segments (`/a/b/..`) are not resolved before matching.
- Repeated slashes (`//`) are not collapsed before matching.

## Caching

The origin proxy has a real in-process response cache, with a simpler model
than CloudFront's.

| Aspect | Overcast |
| --- | --- |
| Cache key | Distribution ID + percent-encoded path + raw query string |
| Ignored in the key | Request headers, cookies, `Vary`, and every cache-policy key setting |
| TTL source | The matched behaviour's cache policy `DefaultTTL`, else 86400 seconds |
| Ignored for TTL | Origin `Cache-Control` and `Expires`, `MinTTL`, `MaxTTL`, and the legacy per-behaviour `DefaultTTL` |
| Filled by | `GET` with a 2xx response only — a `HEAD` reads the cache but never fills it |
| Purged by | `CreateInvalidation` (path or tag), and deleting the distribution |
| Lifetime | Per process. A restart starts cold, whatever the storage backend |

Cache tags come from the origin response header named by
`CacheTagConfig.HeaderName`, read before any viewer-response function runs.
Tag matching is case-insensitive, and a comma-separated header yields at most
50 tags per response.

## Distributions

| Area | On AWS | Overcast |
| --- | --- | --- |
| `Status` after create or update | `InProgress` until propagation finishes | `Deployed` immediately |
| `ETag` | An opaque token | A quoted version counter, `"1"`, `"2"`, … |
| `CallerReference` reuse with an identical config | `DistributionAlreadyExists` | Returns the existing distribution, `201` |
| `CallerReference` reuse with a different config | `DistributionAlreadyExists` | `DistributionAlreadyExists` |
| `DeleteDistribution` | Requires `If-Match` and `Enabled: false` | The same, plus it cascades the distribution's invalidations and purges its cache. Its tag set is not removed |
| `TestFunction` | Runs the code against the supplied event | Returns a fixed success result with `ComputeUtilization: 12`; the code is not run |

`CreateDistributionWithTags` honours a `_custom_id_` tag to force a specific
distribution ID, for tests that need a stable one.

## CloudFront Functions

Viewer-request and viewer-response functions are executed on the proxy path by
an embedded JavaScript engine. Three divergences:

- **Stage is not checked.** The function is resolved by ARN, so a
  `DEVELOPMENT` version attached to a behaviour runs on live requests without
  being published.
- **`TestFunction` does not execute anything.** Use a real request through the
  distribution to exercise a function.
- **Viewer-response functions do not run on a cache hit.** They run only when
  the response comes from the origin. On AWS they run on every response,
  including one served from the cache.

Lambda@Edge is not emulated at all.

## Scope and state

Distribution state is partitioned by the region in the caller's SigV4
credential scope, even though CloudFront is a global service on AWS — the
hostname grammar is global (`{id}.cloudfront.{host}`, no region label) but the
store is not.

> [!WARNING]
> Requests through the origin proxy are unsigned, so they fall back to the
> configured default region. A distribution created by a client signing for a
> different region will not be found by the proxy. Keep `AWS_REGION` consistent
> between the client that creates the distribution and `OVERCAST_REGION`.

## Related

- [CloudFront](../cloudfront.md) — quick start and what works
- [CloudFront operations](./operations.md) — per-operation status
- [Networking and host-based addressing](../../networking.md)
