---
title: "RDS limitations"
description: "Which RDS engines Overcast runs and which it cannot, what an Aurora member inherits from its cluster, which cluster settings are enforced, and how a cluster endpoint is named."
section: "Service Reference"
tags:
  - database
  - docs
  - limitations
  - rds
  - services
---

# RDS limitations

The full divergence list behind [RDS](../rds.md). Everything here applies to
`live` mode, the default; `mock` mode adds only that no engine exists at all.

## Engines

| Engine | AWS value | Default version | Images |
| --- | --- | --- | --- |
| PostgreSQL | `postgres` | 16.1 | `postgres:16` (16.1), `postgres:15` (15.5), `postgres:14` (14.11) |
| MySQL | `mysql` | 8.0 | `mysql:8.0`, `mysql:8.4`, `mysql:5.7` |
| MariaDB | `mariadb` | 11.4 | `mariadb:11` (11.4), `mariadb:10.11` |
| Aurora MySQL | `aurora-mysql` | 3.04 | `mysql:8.0` (3.04), `mysql:8.4` (4.0), `mysql:5.7` (2.11) |
| Aurora PostgreSQL | `aurora-postgresql` | 15.4 | `postgres:15` (15.4), `postgres:14` (14.11) |

Any `EngineVersion` is accepted. A version with no image of its own is served by
the nearest one in its family — `8.0.39` runs `mysql:8.0`, `16.3` runs
`postgres:16` — and the substitution is logged. Real stacks send precise
versions, such as CDK's `MysqlEngineVersion.VER_8_0_39`.

Aurora uses its open-source counterpart's image because both speak the same wire
protocol. The Aurora resource model — `CreateDBCluster`, then
`CreateDBInstance` with a `DBClusterIdentifier` — is fully supported, and a
container starts per member instance.

### Engines that are not emulated

| Engine | Why |
| --- | --- |
| `sqlserver-*` | Not yet implemented. A free image exists (`mcr.microsoft.com/mssql/server`), so this is planned |
| `db2-ae`, `db2-se` | Not yet implemented. A community image exists (`icr.io/db2_community/db2`), but Db2 is rare in local development |
| `oracle-*`, `custom-oracle-*` | Not feasible. Oracle's images need a commercial licence and cannot be redistributed |

## What an Aurora member inherits

A `CreateDBInstance` naming a `DBClusterIdentifier` takes these from the cluster,
and conflicting instance-level values are ignored so they cannot silently change
which engine or credentials the endpoint serves. The instance engine must match
the cluster engine.

| Field | Effective member value |
| --- | --- |
| `DBSubnetGroupName` | The cluster's, and with it the cluster's VPC |
| `MasterUsername`, `MasterUserPassword` | The cluster's |
| `EngineVersion`, `Port` | The cluster's |
| `DBName` | The cluster's `DatabaseName` |

`rds.DatabaseCluster` synthesises an `AWS::RDS::DBInstance` with a
`DBClusterIdentifier` and nothing else, because on AWS the cluster supplies the
rest.

An inherited subnet group counts as the instance's own for the
`PubliclyAccessible` defaults below, so a member of a cluster in a subnet group
is private by default. Set `PubliclyAccessible=true` on the **member**, not the
cluster — AWS has no cluster-level field, and neither does Overcast.

## What a cluster records and what it enforces

`ModifyDBCluster` accepts the settings CloudFormation sends and
`DescribeDBClusters` reports them back. What sits behind each one differs.

| Setting | Behaviour |
| --- | --- |
| `MasterUserPassword` | Applied to every member's engine |
| `ManageMasterUserPassword` | **Enforced** — generates a password and a Secrets Manager secret, returned as `MasterUserSecret`; see [master account and passwords](./master-account.md#managed-master-password) |
| `EngineVersion`, `Port` | Recorded and reported |
| `DeletionProtection` | **Enforced** — `DeleteDBCluster` refuses a protected cluster, and a stack delete fails rather than removing it |
| `BackupRetentionPeriod` | **Validated** to AWS's 1–35, defaulting to 1, then recorded only — Overcast takes no backups |
| `PreferredBackupWindow`, `PreferredMaintenanceWindow` | Recorded only |
| `DBClusterParameterGroupName` | Recorded only, and not checked against an existing group — Overcast implements no cluster parameter group operations |
| `VpcSecurityGroupIds` | Recorded only — security groups are not enforced against a database |
| `EnableCloudwatchLogsExports` | Recorded only — no engine log is shipped to CloudWatch Logs |
| `EnableHttpEndpoint` | Recorded and reported as `HttpEndpointEnabled` — no Data API exists to actually reach |
| `ServerlessV2ScalingConfiguration` | Recorded and reported — every member still runs as a fixed-size container regardless |
| `StorageEncrypted`, `KmsKeyId` | Create-only, recorded and reported — no real encryption happens |

On the CloudFormation side, `Engine`, `MasterUsername`, `DatabaseName`,
`DBSubnetGroupName` and `KmsKeyId` carry "Update requires: Replacement" on AWS
and force replacement here too, as does a `StorageEncrypted` value that
differs from the template's previous deploy.

## What a DB instance records and what it enforces

`ModifyDBInstance` accepts these settings — except `StorageEncrypted`,
`KmsKeyId` and `AvailabilityZone`, which AWS itself only ever takes at create —
and `DescribeDBInstances` reports them back with AWS's own field names and
defaults. None of them changes runtime behaviour beyond `DeletionProtection`.

| Setting | Behaviour |
| --- | --- |
| `DeletionProtection` | **Enforced** — `DeleteDBInstance` refuses a protected instance, the same way `DeleteDBCluster` refuses a protected cluster |
| `BackupRetentionPeriod` | **Validated** to AWS's 0–35 (0 disables automated backups), defaulting to 1, then recorded only |
| `AutoMinorVersionUpgrade` | Recorded and reported; defaults to `true` |
| `CopyTagsToSnapshot`, `MonitoringInterval`, `EnablePerformanceInsights` (reported as `PerformanceInsightsEnabled`), `EnableIAMDatabaseAuthentication` (reported as `IAMDatabaseAuthenticationEnabled`) | Recorded and reported; each defaults to AWS's own resting value (`false`, except `MonitoringInterval`'s `0`) |
| `PreferredBackupWindow`, `PreferredMaintenanceWindow`, `Iops`, `AvailabilityZone`, `CACertificateIdentifier` | Recorded only |
| `EnableCloudwatchLogsExports` | Recorded only — no engine log is shipped to CloudWatch Logs |
| `StorageEncrypted`, `KmsKeyId` | Create-only, recorded and reported — no real encryption happens |

On the CloudFormation side, `DBInstanceIdentifier`, `Engine`, `MasterUsername`,
`DBName` and `KmsKeyId` carry "Update requires: Replacement" on AWS and force
replacement here too, as does a `StorageEncrypted` value that differs from the
template's previous deploy. `AvailabilityZone` is not applied by an update —
AWS moves it through a Multi-AZ failover rather than through
`ModifyDBInstance`'s own parameters, a mechanism Overcast does not emulate.

## Reachability defaults

`PubliclyAccessible` says whether a database is reachable from outside the VPC it
was placed in. Omit it and it defaults the way AWS defaults it:

| Created | Default | Why |
| --- | --- | --- |
| Without `DBSubnetGroupName` | `true` for RDS, `false` for Aurora | RDS instances use the default VPC, which has an internet gateway; AWS documents a private default for Aurora |
| With `DBSubnetGroupName` | `false` | A named subnet group is a chosen placement, and its gateway state cannot currently be resolved from RDS |

Send `PubliclyAccessible=true` to override it. An instance created before
Overcast had the field reports the value its create would have been given.

## Cluster endpoint names

```
{dbClusterIdentifier}.cluster.{region}.rds.{base}      # writer
{dbClusterIdentifier}.cluster-ro.{region}.rds.{base}   # reader
```

Both drop AWS's account-specific hash, as every Overcast endpoint name does.
The writer label was `cluster-rw` until 0.0.1-alpha.37 — a spelling AWS has
never used. Read the name from `DescribeDBClusters` rather than reconstructing
it: a container that predates the upgrade keeps the alias set it was created
with, so the current names begin resolving when that container is recreated.

Deleting the **writer** promotes a survivor, because AWS treats losing the
primary as a failover. The replacement is chosen by `PromotionTier` (0 is the
highest priority, 15 the lowest, 1 the default), ties going to the oldest
surviving member; AWS breaks the same tie on instance size, which has no
equivalent here because every member runs the same container whatever its
`DBInstanceClass`.

Deleting the last member leaves a cluster with no writer. That is a legitimate
state — CloudFormation deletes a cluster's instances before the cluster itself —
and the endpoints keep their names and the cluster's own port.

## Related

- [RDS](../rds.md) — quick start and what works
- [RDS master account and passwords](./master-account.md) — privileges, password rules, rotation
- [RDS troubleshooting](./troubleshooting.md) — symptom, cause, fix
- [Data-plane endpoints](../../networking/data-plane-endpoints.md) — what an endpoint address resolves to
