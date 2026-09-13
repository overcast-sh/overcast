---
title: "Data-plane endpoints"
description: "RDS, Aurora and ElastiCache endpoints name a container Overcast started, not Overcast. How the name resolves inside a Lambda or task, and why the port differs by caller."
section: "Networking"
tags:
  - aurora
  - docs
  - elasticache
  - endpoints
  - networking
  - rds
---

# Data-plane endpoints

Most names Overcast hands back point at **Overcast**. A few point at a
**container Overcast started** — an RDS instance's `Endpoint.Address`, an
ElastiCache node's address — and those resolve by a different mechanism and
answer on a different port depending on who is asking.

```
{dbInstanceIdentifier}.{region}.rds.{base}             # RDS DB instance
{dbClusterIdentifier}.cluster.{region}.rds.{base}      # Aurora writer
{dbClusterIdentifier}.cluster-ro.{region}.rds.{base}   # Aurora reader
{cacheClusterId}.{region}.cfg.{base}                   # ElastiCache cache cluster
{replicationGroupId}.{region}.ng.cfg.{base}            # ElastiCache replication group
{serverlessCacheName}.{region}.serverless.{base}       # ElastiCache serverless cache
```

`{base}` is `OVERCAST_HOSTNAME` when set, otherwise the host you called Overcast
on — the precedence [every URL follows](./urls.md). With
`OVERCAST_HOSTNAME=localhost.overcast.sh`, a `Fn::GetAtt Endpoint.Address` comes
back as `mydb.ap-southeast-2.rds.localhost.overcast.sh`, and that is the value a
CDK stack bakes into an ECS task definition or a Secrets Manager secret.

| Caller | `Endpoint.Address` | `Endpoint.Port` |
| --- | --- | --- |
| Lambda function, ECS task, any sibling container | the endpoint hostname | the engine port (3306/5432, 6379/11211), as on AWS |
| The host (CLI, SDK, `cdk deploy`) | the endpoint hostname, or `127.0.0.1` when `{base}` has no wildcard DNS | the published host port (`RDS_PORT_BASE`, 33060 upwards; `ELASTICACHE_PORT_BASE`, 63790 upwards) |

The same table holds for ElastiCache's `ConfigurationEndpoint`, `RedisEndpoint`
and a serverless cache's `Endpoint`, and it is applied on every read, not once
at create: a function that discovers its cache through `DescribeCacheClusters`
at runtime is given a name it can dial, not the address Overcast itself uses.

Both pairs connect. Which one you were given is decided by the source address of
your request: a split-horizon hostname is used from both sides of the container
boundary and cannot say which side you are on. The engine listens on 3306/5432
inside the Docker network, and 3306 is often taken by a local install, which is
why the host port starts at 33060 instead.

## How the name resolves inside a container

The engine container carries its endpoint name as a **Docker network alias** on
every network emulated compute runs on — the shared data plane
(`OVERCAST_NETWORK`, default `overcast`), or the VPC network of its DB or cache
subnet group when it has one (both, for an RDS instance that is
`PubliclyAccessible`). Docker's embedded resolver answers from those aliases
before forwarding anything upstream, so Overcast's own DNS server is never
involved: that one answers where *Overcast* is.

The alias set covers the name under every hostname Overcast could mint it under,
because the name a caller holds depends on the endpoint that caller used.

> [!WARNING]
> **A host-side deploy bakes the host-side port into container environment.**
> `cdk deploy` runs on the host, so `Fn::GetAtt Endpoint.Port` resolves to the
> published port, and a task started from that template later reads it from
> inside the network where only 3306 is open. Applications that take a *host* and
> assume the standard port — most of them, including the Bitnami images — are
> unaffected. If you pass the port through explicitly, hard-code the engine's
> standard port rather than `Endpoint.Port`; it is what real AWS would have
> returned anyway.

## Aurora cluster endpoints

A cluster has no container of its own. `Endpoint` and `ReaderEndpoint` both name
the writer member's engine, so `DescribeDBClusters` answers with that instance's
address and port on the rules above, and both cluster names are registered as
aliases on the writer's container — so `cluster.clusterEndpoint.hostname` in a
CDK stack resolves from inside a task exactly as the instance endpoint does.

**The reader endpoint does not load-balance.** On AWS it
load-balances across the Aurora Replicas and serves the writer only when the
cluster has none. Overcast gives every cluster member its own engine container
with its own storage — there is no shared Aurora volume to replicate from — so a
reader endpoint spread across the replicas would answer from an empty database.
It points at the writer: reads are not distributed, and they return the data that
was written.

The names drop AWS's account-specific hash, as every Overcast endpoint name does,
so `{cluster}.cluster-{hash}.…` and `{cluster}.cluster-ro-{hash}.…` reduce to the
two forms above. Overcast minted `cluster-rw` for the writer until
0.0.1-alpha.37, a label AWS has never used; a cluster created by an older
Overcast keeps answering to the name in its stored record, so an upgrade in place
strands nothing, and a cluster created since answers only to `cluster`.

## Related

- [Networking troubleshooting](./troubleshooting.md) — when an endpoint name resolves nowhere
- [RDS](../services/rds.md) — the service reference
- [The Docker networks Overcast uses](./docker-networks.md) — which network the alias lands on
- [Networking and host-based addressing](../networking.md) — the rest of the addressing story
