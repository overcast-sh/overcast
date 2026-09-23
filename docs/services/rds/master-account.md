---
title: "RDS master account and passwords"
description: "What the master account Overcast creates can and cannot do on each engine, the per-engine password rules, and how a password change reaches a running database."
section: "Service Reference"
tags:
  - database
  - docs
  - passwords
  - rds
  - services
---

# RDS master account and passwords

The administrator [RDS](../rds.md) creates for you, what it is allowed to do on
each engine, and what a password has to look like.

## Master account boundaries

The requested master account is the administrator your application connects as.
On MySQL, MariaDB and Aurora MySQL it can create databases and users and grant
privileges across the instance; the grants follow the selected engine version
(the `rds_superuser_role` model on RDS MySQL 8.0.36+ and Aurora MySQL 3, the
revised dynamic privileges and `caching_sha2_password` on 8.4). On PostgreSQL
and Aurora PostgreSQL it is a non-superuser with `CREATEDB`, `CREATEROLE` and
membership in the emulated `rds_superuser` role, matching the boundary AWS
exposes rather than the stock image's unrestricted superuser.

What this does **not** emulate: AWS's full catalogue of protected internal
accounts and `rds_*` procedures. PostgreSQL extension availability follows the
backing image rather than the RDS extension allowlist, and reserved-word
validation covers the engine system schemas and common SQL keywords rather than
every version-specific reserved word. Code that depends on those administrative
edges still needs testing against AWS.

The container's maintenance account is separate: Overcast uses it during
initialisation and password recovery, its generated credential is never returned
by the API, and it is not an alternative application credential.

`DBName` follows the engine's AWS behaviour. MySQL and MariaDB create no
application database when it is omitted; PostgreSQL always has `postgres`, and
an explicit `DBName` creates an additional database owned by the master account.

## Password rules

| Engine | Length |
| --- | --- |
| MySQL, MariaDB, Aurora MySQL | 8–41 |
| RDS PostgreSQL | 8–128 |
| Aurora PostgreSQL | 8–99 |

All accept printable ASCII except `/`, `"`, `@` and space. A single quote is
valid and is escaped before it reaches the engine.

> [!IMPORTANT]
> `GetRandomPassword`'s default punctuation set contains characters RDS forbids,
> as AWS's does — which is why CDK's `Credentials.fromGeneratedSecret` excludes
> them by default. If a `{{resolve:secretsmanager:…}}` password is refused, set
> `ExcludeCharacters` on the generated secret, as you would for AWS.

## Managed master password

`ManageMasterUserPassword: true` skips `MasterUserPassword` entirely: RDS
generates a password that satisfies the table above, stores it in a Secrets
Manager secret named `rds!db-<uuid>` (`rds!cluster-<uuid>` for a cluster), and
returns that secret's ARN as `MasterUserSecret` on every response that would
otherwise carry the password. An Aurora member instance reports its cluster's
secret rather than minting one of its own — AWS creates exactly one secret per
cluster.

The secret holds `{"username": "...", "password": "..."}` and no connection
details, as on AWS. Read the host and port from `Endpoint` in
`DescribeDBInstances` (or `DescribeDBClusters`).

`ModifyDBInstance`/`ModifyDBCluster` can turn the option on, which generates a
new password, or off, which requires `MasterUserPassword` in the same call to
hand ownership of the password back to the caller. Supplying
`MasterUserPassword` while the password stays managed is refused, the same
combination `CreateDBInstance` refuses. Turning management off deletes the
secret, as does deleting the DB instance or cluster itself.

## Password changes

`MasterUserPassword` is applied to the running database rather than only
recorded, so rotating one in a CloudFormation template takes effect and never
replaces the instance. The instance must be `available` for it, and with Docker
unavailable the password is stored and seeded into the container built for that
instance later. `ModifyDBCluster` rotates each member in turn — see
[`ModifyDBInstance` refuses the new password](./troubleshooting.md#modifydbinstance-refuses-the-new-password).

## Related

- [RDS](../rds.md) — quick start and what works
- [RDS limitations](./limitations.md) — engines, cluster settings and endpoint names
- [RDS troubleshooting](./troubleshooting.md) — symptom, cause, fix
- [RDS operations](./operations.md) — per-operation status
