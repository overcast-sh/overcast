---
title: "OpenSearch — Amazon OpenSearch Service"
description: "Quick start, the domain and tag operations that work, and why the domain endpoint answers nothing: no cluster, no cluster settings, no configuration changes."
section: "Service Reference"
tags:
  - amazon
  - docs
  - opensearch
  - service
  - services
---

# OpenSearch — Amazon OpenSearch Service

Domains are control-plane records only: no search cluster is started, so
`DomainStatus.Endpoint` names a host that answers nothing.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws opensearch create-domain \
  --domain-name logs \
  --engine-version OpenSearch_2.11
aws opensearch describe-domain --domain-name logs
aws opensearch list-domain-names
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Domains | Create, describe, batch-describe, list and delete; active the moment they are created |
| Name collisions | A repeat domain name in the same region is rejected |
| Input validation | `DomainName` (3-28 characters of `[a-z][a-z0-9-]`) and `EngineVersion` (`OpenSearch_X.Y` or `Elasticsearch_X.Y`) are checked, as AWS checks them |
| Regions | Domains are per-region — the same name in two regions is two domains |
| Filtering | `ListDomainNames --engine-type` is honoured, derived from each domain's `EngineVersion` |
| Tags | Inline `TagList` at creation, plus `AddTags`, `ListTags` and `RemoveTags`; deleting a domain deletes its tags |

## Differences from AWS

| Area                  | On AWS                                                            | Overcast                                                                                                                                                |
| --------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The cluster           | Indexes documents and serves queries                              | Nothing is indexed or queried; the domain endpoint is a name, not a service                                                                             |
| `DomainStatus`        | The full shape                                                    | The four required members plus `EngineVersion`, `Endpoint` and the `Created`/`Deleted`/`Processing` flags; the rest is omitted                          |
| Cluster settings      | Configure the cluster                                             | `ClusterConfig` is echoed back unchanged; `EBSOptions`, `VPCOptions`, access policies and the other ~25 `CreateDomain` members are accepted and ignored |
| `Endpoint`            | A live domain host on port 443                                    | An AWS-shaped hostname that nothing answers on                                                                                                          |
| Configuration changes | `UpdateDomainConfig`, upgrades, package association and auto-tune | Not implemented                                                                                                                                         |
| Cross-cluster search  | Outbound and inbound connections                                  | Not modelled                                                                                                                                            |

## Gotchas

> [!WARNING]
> Client libraries that talk the OpenSearch REST API (index, search, bulk)
> address the domain endpoint directly, not this control plane. Those calls
> reach nothing here — run a real OpenSearch container alongside Overcast if
> your test needs to index documents.

`DomainStatus.Endpoint` is a bare hostname, the way AWS reports one, and a
domain's data plane lives on that host rather than on a path beneath the
control-plane endpoint. There is no prefix to point a search client at: the
management operations above are the whole of what Overcast serves.

<!-- BEGIN overcast:capabilities -->

## Operations

All 8 listed operations are implemented.
Per-operation status, notes and AWS API links: [OpenSearch operations](opensearch/operations.md).

<!-- END overcast:capabilities -->

## Related

- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/opensearch-service/latest/APIReference/)
