+ [rds] `CreateDBInstance`/`ModifyDBInstance` accept and echo back the dropped DBInstance properties with AWS's own field names, defaults and shapes.
  `StorageEncrypted`, `KmsKeyId`, `DeletionProtection`, `BackupRetentionPeriod`, `PreferredBackupWindow`, `PreferredMaintenanceWindow`, `AutoMinorVersionUpgrade`.
  `Iops`, `AvailabilityZone`, `EnableIAMDatabaseAuthentication`, `CACertificateIdentifier`, `MonitoringInterval`, `EnablePerformanceInsights`.
  `EnableCloudwatchLogsExports` and `CopyTagsToSnapshot`.
*! [rds] `DeleteDBInstance` refuses an instance with `DeletionProtection` enabled, as AWS does.
  the flag used to be ignored; `DeleteDBCluster` already refused a protected cluster.
  migration: set `DeletionProtection` to false with `ModifyDBInstance` (or in the template) before deleting the instance or its stack.
+ [rds] `CreateDBCluster`/`ModifyDBCluster` accept and echo back `StorageEncrypted`, `KmsKeyId`, `EnableHttpEndpoint` and `ServerlessV2ScalingConfiguration`.
* [cloudformation] `AWS::RDS::DBInstance`/`AWS::RDS::DBCluster` forward the same properties on Create and Update.
  forces replacement for `KmsKeyId` and a changed `StorageEncrypted` — both create-only on AWS.
