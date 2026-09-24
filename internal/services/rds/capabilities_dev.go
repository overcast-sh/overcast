//go:build dev

package rds

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// DB instances
		capabilities.Capability{Service: "rds", Operation: "CreateDBInstance", Category: "DB instances", Status: capabilities.StatusSupported, Notes: "Docker-backed when available; async creating→available; mysql/postgres/mariadb/aurora-mysql/aurora-postgresql; master accounts can create databases/users and grant privileges; omitted `DBName` creates no MySQL/MariaDB database; Aurora members use cluster-owned placement, credentials, version, port, and database; omitted `PubliclyAccessible` defaults to false for Aurora and otherwise follows subnet placement; `ManageMasterUserPassword` generates a password and creates a Secrets Manager secret (`rds!db-<uuid>`), returned as `MasterUserSecret`; `Tags` applied via the shared tag store; `StorageEncrypted`, `KmsKeyId`, `DeletionProtection`, `BackupRetentionPeriod`, `PreferredBackupWindow`, `PreferredMaintenanceWindow`, `AutoMinorVersionUpgrade`, `Iops`, `AvailabilityZone`, `EnableIAMDatabaseAuthentication`, `CACertificateIdentifier`, `MonitoringInterval`, `EnablePerformanceInsights`, `EnableCloudwatchLogsExports` and `CopyTagsToSnapshot` are accepted, stored and echoed back with AWS's own defaults where unset"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBInstances", Category: "DB instances", Status: capabilities.StatusSupported, Notes: "List all or filter by DBInstanceIdentifier"},
		capabilities.Capability{Service: "rds", Operation: "DeleteDBInstance", Category: "DB instances", Status: capabilities.StatusSupported, Notes: "Sets status to \"deleting\"; stops+removes Docker container; deletes the instance's own Secrets Manager secret when `ManageMasterUserPassword` was set (never a secret inherited from an Aurora cluster); refuses an instance with `DeletionProtection` enabled"},
		capabilities.Capability{Service: "rds", Operation: "StopDBInstance", Category: "DB instances", Status: capabilities.StatusSupported, Notes: "Stops Docker container; available→stopping→stopped"},
		capabilities.Capability{Service: "rds", Operation: "StartDBInstance", Category: "DB instances", Status: capabilities.StatusSupported, Notes: "Starts Docker container; stopped→starting→available"},
		capabilities.Capability{Service: "rds", Operation: "ModifyDBInstance", Category: "DB instances", Status: capabilities.StatusSupported, Notes: "Metadata updates (class, storage, engine version, multi-AZ, public accessibility); `MasterUserPassword` is applied to the running engine, requires an `available` instance, and uses RDS's engine-specific length and forbidden-character rules; `ManageMasterUserPassword` turns the managed-secret path on (generating a new password) or off (requires `MasterUserPassword`); `DeletionProtection`, `BackupRetentionPeriod`, `PreferredBackupWindow`, `PreferredMaintenanceWindow`, `AutoMinorVersionUpgrade`, `Iops`, `EnableIAMDatabaseAuthentication`, `CACertificateIdentifier`, `MonitoringInterval`, `EnablePerformanceInsights`, `CloudwatchLogsExportConfiguration` and `CopyTagsToSnapshot` are applied and echoed back; `StorageEncrypted`/`KmsKeyId`/`AvailabilityZone` are create-only, as on AWS"},
		capabilities.Capability{Service: "rds", Operation: "RebootDBInstance", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "CreateDBSnapshot", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "DeleteDBSnapshot", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBSnapshots", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "RestoreDBInstanceFromDBSnapshot", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBLogFiles", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "DownloadDBLogFilePortion", Category: "DB instances", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Aurora clusters
		capabilities.Capability{Service: "rds", Operation: "CreateDBCluster", Category: "Aurora clusters", Status: capabilities.StatusSupported, Notes: "aurora-mysql and aurora-postgresql only; logical cluster, Docker started on first instance; `ManageMasterUserPassword` generates a password and creates a Secrets Manager secret (`rds!cluster-<uuid>`), returned as `MasterUserSecret` and inherited by every member instance; `Tags` applied via the shared tag store; `StorageEncrypted`, `KmsKeyId`, `EnableHttpEndpoint` and `ServerlessV2ScalingConfiguration` are accepted, stored and echoed back"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBClusters", Category: "Aurora clusters", Status: capabilities.StatusSupported, Notes: "List all or filter by DBClusterIdentifier; returns cluster members"},
		capabilities.Capability{Service: "rds", Operation: "DeleteDBCluster", Category: "Aurora clusters", Status: capabilities.StatusSupported, Notes: "Sets status to \"deleting\"; async removal; refuses a cluster with `DeletionProtection` enabled; deletes the cluster's Secrets Manager secret when `ManageMasterUserPassword` was set"},
		capabilities.Capability{Service: "rds", Operation: "ModifyDBCluster", Category: "Aurora clusters", Status: capabilities.StatusSupported, Notes: "`MasterUserPassword` applied to every member's engine; engine version, port and `DeletionProtection` applied; backup/maintenance windows, cluster parameter group, security groups and log exports recorded; `ManageMasterUserPassword` turns the managed-secret path on or off the same way ModifyDBInstance does; `EnableHttpEndpoint` and `ServerlessV2ScalingConfiguration` are applied and echoed back; `StorageEncrypted`/`KmsKeyId` are create-only, as on AWS"},
		capabilities.Capability{Service: "rds", Operation: "StartDBCluster", Category: "Aurora clusters", Status: capabilities.StatusSupported, Notes: "stopped→starting→available"},
		capabilities.Capability{Service: "rds", Operation: "StopDBCluster", Category: "Aurora clusters", Status: capabilities.StatusSupported, Notes: "available→stopping→stopped"},
		capabilities.Capability{Service: "rds", Operation: "CreateDBClusterSnapshot", Category: "Aurora clusters", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "DeleteDBClusterSnapshot", Category: "Aurora clusters", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBClusterSnapshots", Category: "Aurora clusters", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Events
		capabilities.Capability{Service: "rds", Operation: "DescribeEvents", Category: "Events", Status: capabilities.StatusSupported, Notes: "db-instance events for create/start/stop/delete/failure; 14-day retention, 60-minute default window; `SourceIdentifier`, `SourceType`, `EventCategories`, `StartTime`/`EndTime`/`Duration`, `Marker`/`MaxRecords`"},
		// Engine metadata
		capabilities.Capability{Service: "rds", Operation: "DescribeDBEngineVersions", Category: "Engine metadata", Status: capabilities.StatusSupported, Notes: "mysql (8.4, 8.0, 5.7), postgres (16.1, 15.5, 14.11), mariadb (11.4, 10.11), aurora-mysql (4.0, 3.04, 2.11), aurora-postgresql (15.4, 14.11)"},
		capabilities.Capability{Service: "rds", Operation: "DescribeOrderableDBInstanceOptions", Category: "Engine metadata", Status: capabilities.StatusSupported, Notes: "Static list of engine + instance class combos for mysql/postgres/mariadb"},
		// Subnet groups
		capabilities.Capability{Service: "rds", Operation: "CreateDBSubnetGroup", Category: "Subnet groups", Status: capabilities.StatusSupported, Notes: "Metadata-only; stores subnet IDs and VPC ID"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBSubnetGroups", Category: "Subnet groups", Status: capabilities.StatusSupported, Notes: "List all or filter by name"},
		capabilities.Capability{Service: "rds", Operation: "DeleteDBSubnetGroup", Category: "Subnet groups", Status: capabilities.StatusSupported},
		// Parameter groups
		capabilities.Capability{Service: "rds", Operation: "CreateDBParameterGroup", Category: "Parameter groups", Status: capabilities.StatusSupported, Notes: "Validates family against known engines; stores in state"},
		capabilities.Capability{Service: "rds", Operation: "DescribeDBParameterGroups", Category: "Parameter groups", Status: capabilities.StatusSupported, Notes: "List all or filter by name"},
		capabilities.Capability{Service: "rds", Operation: "DeleteDBParameterGroup", Category: "Parameter groups", Status: capabilities.StatusSupported},
		// General
		capabilities.Capability{Service: "rds", Operation: "AddTagsToResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Tags stored per-ARN in `rds:tags` namespace; shared tag validation"},
		capabilities.Capability{Service: "rds", Operation: "ListTagsForResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Returns tag list for any RDS resource ARN"},
		capabilities.Capability{Service: "rds", Operation: "RemoveTagsFromResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Removes specified tag keys from a resource"},
	)
}
