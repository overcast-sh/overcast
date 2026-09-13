---
title: "RDS — Relational Database Service"
description: "Quick start, the engines and Aurora shapes started for real, readiness and failure reporting, per-caller endpoints, and the backups, replicas and failover that are absent."
section: "Service Reference"
tags:
  - database
  - docs
  - rds
  - relational
  - service
  - services
---

# RDS — Relational Database Service

`CreateDBInstance` starts a real database container, and the instance reports
`available` only once that engine is genuinely accepting connections.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws rds create-db-instance --db-instance-identifier mydb \
  --engine postgres --db-instance-class db.t3.micro \
  --allocated-storage 20 --master-username app --master-user-password secret99

aws rds wait db-instance-available --db-instance-identifier mydb
aws rds describe-db-instances --db-instance-identifier mydb \
  --query 'DBInstances[0].Endpoint'
# → { "Address": "127.0.0.1", "Port": 33060 }  on the host
psql -h 127.0.0.1 -p 33060 -U app postgres
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

First boot takes roughly half a minute — the engine has to initialise its data
directory before it accepts anything.

## What works

| Area | Behaviour |
| --- | --- |
| Real engines | MySQL, PostgreSQL, MariaDB and both Aurora variants, in containers, with ports allocated from `RDS_PORT_BASE` (default 33060) |
| Honest readiness | `available` means Overcast opened a TCP connection, ran the credential initialisation, and the engine's own readiness client confirmed the final server is listening — on create and on start alike |
| Failure is terminal | A container that exits, cannot be created, or misses engine readiness within five minutes goes to `failed`. Nothing is left in `creating` or `starting` forever |
| Per-caller endpoints | `{id}.{region}.rds.{base}` resolves to the engine container from a Lambda or ECS task, on the engine's own port; the host gets the published port. One stack output works from both sides |
| Aurora | Clusters, member instances that inherit the cluster's placement and credentials, writer promotion on deleting the writer, and both cluster endpoint names |
| Master account | A real administrator account with the privileges AWS grants — `CREATEDB`/`CREATEROLE`/`rds_superuser` on PostgreSQL, the version-appropriate grant set on the MySQL family |
| Password rotation | `ModifyDBInstance` and `ModifyDBCluster` run the engine's own `ALTER USER`, so the old password really stops working |
| Reachability | `PubliclyAccessible` decides it, not just metadata — an instance in a subnet group is reachable only from that VPC unless it is public, in which case it stays on the shared data plane too. `ModifyDBInstance` re-attaches a running container when the flag changes |
| CloudFormation | A `DBInstance` or `DBCluster` is not `CREATE_COMPLETE` until the database is `available`, so anything downstream of it waits, as on AWS |
| Diagnostics | `DescribeEvents` records the lifecycle, including why an instance failed; the console's Logs tab reads the container's output |
| Persistence | Containers survive an Overcast restart, and one Overcast never reclaims another's — engine containers carry the identity of the state store that created them |

## Differences from AWS

| Area                                                                           | On AWS                                       | Overcast                                                                                                                     |
| ------------------------------------------------------------------------------ | -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Reader endpoint                                                                | Serves the replicas                          | Serves the writer — every member has its own storage, so reads are not distributed and replica lag cannot be reproduced      |
| Backup and maintenance windows, parameter groups, security groups, log exports | Acted on                                     | Stored and reported, never acted on                                                                                          |
| Snapshots                                                                      | Snapshot, restore and point-in-time recovery | Not implemented                                                                                                              |
| `EngineVersion`                                                                | The exact version is provisioned             | Any version is accepted; one with no image of its own is served by the nearest in its family, and the substitution is logged |
| Engines                                                                        | SQL Server, Oracle, Db2 and the rest         | PostgreSQL, MySQL, MariaDB and Aurora only — see [RDS limitations](./rds/limitations.md)                                     |
| Failover                                                                       | `FailoverDBCluster` promotes a replica       | No such operation; deleting the writer is the only way to trigger a promotion                                                |

Full engine matrix and cluster-setting behaviour:
[RDS limitations](./rds/limitations.md). Privileges, password rules and rotation:
[RDS master account and passwords](./rds/master-account.md). Symptoms and fixes:
[RDS troubleshooting](./rds/troubleshooting.md).

## Gotchas

`OVERCAST_RDS_MODE=mock` reaches `available` in a moment and starts no
container, while `DescribeDBInstances` still reports an address and port with
nothing listening on them. Use it when you are testing the control plane; keep
the default if your code connects.

> [!CAUTION]
> Promoting a new writer moves both cluster endpoint names onto its container,
> which means detaching and re-attaching it to its Docker networks. Connections
> held open to that container over those networks are dropped — the same
> interruption a real failover causes.

<!-- BEGIN overcast:capabilities -->

## Operations

24 of 34 listed operations are implemented.
Per-operation status, notes and AWS API links: [RDS operations](rds/operations.md).

<!-- END overcast:capabilities -->

## Related

- [RDS limitations](./rds/limitations.md)
- [RDS master account and passwords](./rds/master-account.md)
- [RDS troubleshooting](./rds/troubleshooting.md)
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [Networking § data-plane endpoints](../networking/data-plane-endpoints.md)
- [AWS API reference](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/Welcome.html)
