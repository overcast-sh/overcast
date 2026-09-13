---
title: "ElastiCache — Managed In-Memory Cache"
description: "Quick start, the engines started per cache, how readiness and failure are reported, VPC placement and per-caller endpoints, and the scaling and snapshot gaps."
section: "Service Reference"
tags:
  - cache
  - docs
  - elasticache
  - managed
  - memory
  - services
---

# ElastiCache — Managed In-Memory Cache

Creating a cache starts a real Redis, Valkey or Memcached container; without
Docker the same calls are metadata only.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws elasticache create-cache-cluster \
  --cache-cluster-id sessions \
  --engine redis --cache-node-type cache.t3.micro --num-cache-nodes 1

aws elasticache describe-cache-clusters --cache-cluster-id sessions \
  --query 'CacheClusters[0].[CacheClusterStatus,ConfigurationEndpoint]'
# once "available": ["127.0.0.1", 63790] on the host
redis-cli -p 63790 ping
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Real engines | `CreateCacheCluster`, `CreateReplicationGroup` and `CreateServerlessCache` each start a container, ports allocated from `ELASTICACHE_PORT_BASE` (default 63790) |
| Honest readiness | The status moves to `available` only after the engine answers its own protocol — `PING` for Redis and Valkey, `version` for Memcached. A published port that merely accepts a connection is not enough |
| Failure is terminal | An engine that never answers settles in the status AWS documents for that shape — `incompatible-network` for a cache cluster, `create-failed` for a replication group or serverless cache — with the reason recorded |
| VPC placement | A `CacheSubnetGroupName` puts the cache on that subnet group's VPC network and nothing else; a name Overcast has no record of is refused with `CacheSubnetGroupNotFoundFault`, as on AWS. Without one the cache is in the default VPC, which is the shared data plane |
| Per-caller endpoints | `{id}.{region}.cfg.{base}` resolves to the engine container from a Lambda or ECS task, on the engine's own port; the host gets the published port, and `127.0.0.1` when `{base}` has no wildcard DNS. `DescribeCacheClusters` mints it for whoever asks, so a function reading it at runtime gets a name it can dial — [Data-plane endpoints](../networking/data-plane-endpoints.md) |
| CloudFormation | `AWS::ElastiCache::CacheCluster` and `AWS::ElastiCache::ReplicationGroup`; `Fn::GetAtt` gives `RedisEndpoint.Address`/`.Port` for Redis and Valkey and `ConfigurationEndpoint.Address`/`.Port` for Memcached, the pair each engine has on AWS |
| Without Docker | Every operation still works as metadata, and statuses settle immediately |

Supported engines: **redis** (`redis:6`, `redis:7`), **valkey**
(`valkey/valkey:7`, `valkey/valkey:8`), **memcached** (`memcached:1.5`,
`memcached:1.6`).

## Differences from AWS

| Area                                         | On AWS                                                  | Overcast                                                                                                                |
| -------------------------------------------- | ------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| Cluster size                                 | Replicas, cluster mode and failover                     | A replication group starts a single primary container, always                                                           |
| Replication-group endpoints                  | `PrimaryEndPoint` and `ConfigurationEndPoint` differ    | Both carry the same endpoint — there is nothing to distinguish cluster-mode-enabled from disabled                       |
| Parameter and security groups                | Applied to the engine                                   | `CacheParameterGroupName` is recorded and echoed, never pushed into the engine; `SecurityGroupIds` are dropped entirely |
| Snapshots                                    | Snapshot, backup and restore                            | Not implemented                                                                                                         |
| Scaling                                      | Node-count changes and `IncreaseReplicaCount` add nodes | `ModifyCacheCluster` node-count changes and `IncreaseReplicaCount` do not add containers                                |
| `CacheSubnetGroupName` on `ReplicationGroup` | Not returned                                            | Not returned either                                                                                                     |
| `AWS::ElastiCache::CacheCluster` naming      | The property is `ClusterName`, not `CacheClusterId`     | Omit it and the name is generated from the stack, the logical id and a suffix, lowercased and capped at 50 characters   |

## Gotchas

> [!IMPORTANT]
> A cache in a VPC is reachable only from that VPC, as on AWS — ElastiCache has
> no `PubliclyAccessible` escape hatch. Create both the cache and its caller
> with the same subnet group, or leave both out of one. A `CfnCacheCluster`
> with no `cacheSubnetGroupName` is in the default VPC, and a Fargate task in
> the stack's own VPC cannot resolve it — the `data-plane-name-refused`
> advisory says which two resources disagree. See
> [Lambda, ECS and VPCs](../networking/vpcs.md).

The cache id is constrained too, by Docker rather than by AWS.

> [!WARNING]
> Container names derive from the cache id you choose, and Docker requires them
> to be unique per daemon. Two Overcasts sharing a daemon can each hold a cache
> called `sessions` only if they are given different names — the second to start
> one fails it with a reason saying so, rather than quietly sharing the first's.

<!-- BEGIN overcast:capabilities -->

## Operations

22 of 24 listed operations are implemented.
Per-operation status, notes and AWS API links: [ElastiCache operations](elasticache/operations.md).

<!-- END overcast:capabilities -->

## Related

- [RDS](./rds.md) — the same Docker-backed lifecycle for databases
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [Lambda, ECS and VPCs](../networking/vpcs.md)
- [AWS API reference](https://docs.aws.amazon.com/AmazonElastiCache/latest/APIReference/Welcome.html)
