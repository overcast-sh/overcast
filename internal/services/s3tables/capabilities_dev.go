//go:build dev

package s3tables

import "github.com/overcast-sh/overcast/internal/capabilities"

const (
	catBuckets     = "Table buckets"
	catNamespaces  = "Namespaces"
	catTables      = "Tables"
	catPolicies    = "Policies"
	catEncryption  = "Encryption, storage class and metrics"
	catMaintenance = "Maintenance"
	catReplication = "Replication and record expiration"
	catTags        = "Tags"
	catIceberg     = "Iceberg REST catalog"
)

func init() {
	capabilities.Default.RegisterForService(serviceName,
		// Table buckets
		capabilities.Capability{Operation: "CreateTableBucket", Category: catBuckets,
			Status: capabilities.StatusSupported, Notes: "Table bucket naming rules; `encryptionConfiguration`, `storageClassConfiguration` and `tags` on create"},
		capabilities.Capability{Operation: "GetTableBucket", Category: catBuckets,
			Status: capabilities.StatusSupported, Notes: "Addressed by ARN; `type` is always `customer`"},
		capabilities.Capability{Operation: "ListTableBuckets", Category: catBuckets,
			Status: capabilities.StatusSupported, Notes: "`prefix`, `type`, `maxBuckets` and `continuationToken`"},
		capabilities.Capability{Operation: "DeleteTableBucket", Category: catBuckets,
			Status: capabilities.StatusSupported, Notes: "`ConflictException` while the bucket still holds namespaces"},

		// Namespaces
		capabilities.Capability{Operation: "CreateNamespace", Category: catNamespaces,
			Status: capabilities.StatusSupported, Notes: "One-level namespaces, as AWS; namespace naming rules"},
		capabilities.Capability{Operation: "GetNamespace", Category: catNamespaces,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "ListNamespaces", Category: catNamespaces,
			Status: capabilities.StatusSupported, Notes: "`prefix`, `maxNamespaces` and `continuationToken`"},
		capabilities.Capability{Operation: "DeleteNamespace", Category: catNamespaces,
			Status: capabilities.StatusSupported, Notes: "`ConflictException` while the namespace still holds tables"},

		// Tables
		capabilities.Capability{Operation: "CreateTable", Category: catTables,
			Status: capabilities.StatusPartial, Notes: "Creates the table's `--table-s3` warehouse bucket in S3; `metadata.iceberg.schema` (primitive types) or `schemaV2` (struct, list and map included), `partitionSpec`, `writeOrder` and `properties` write the first `metadata.json`, with field ids reassigned depth-first as Iceberg assigns them"},
		capabilities.Capability{Operation: "GetTable", Category: catTables,
			Status: capabilities.StatusSupported, Notes: "By `tableArn`, or by `tableBucketARN`, `namespace` and `name`"},
		capabilities.Capability{Operation: "ListTables", Category: catTables,
			Status: capabilities.StatusSupported, Notes: "`namespace`, `prefix`, `maxTables` and `continuationToken`"},
		capabilities.Capability{Operation: "DeleteTable", Category: catTables,
			Status: capabilities.StatusSupported, Notes: "Optional `versionToken` check; the warehouse bucket and its objects are left in S3"},
		capabilities.Capability{Operation: "RenameTable", Category: catTables,
			Status: capabilities.StatusSupported, Notes: "New name and/or namespace; the ARN and version token are unchanged"},
		capabilities.Capability{Operation: "GetTableMetadataLocation", Category: catTables,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "UpdateTableMetadataLocation", Category: catTables,
			Status: capabilities.StatusSupported, Notes: "Compare-and-swap on `versionToken`: a stale token is `ConflictException`; the location must be inside the warehouse"},

		// Policies
		capabilities.Capability{Operation: "PutTableBucketPolicy", Category: catPolicies,
			Status: capabilities.StatusInert, Notes: "Stored and returned; not evaluated"},
		capabilities.Capability{Operation: "GetTableBucketPolicy", Category: catPolicies,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "DeleteTableBucketPolicy", Category: catPolicies,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "PutTablePolicy", Category: catPolicies,
			Status: capabilities.StatusInert, Notes: "Stored and returned; not evaluated"},
		capabilities.Capability{Operation: "GetTablePolicy", Category: catPolicies,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "DeleteTablePolicy", Category: catPolicies,
			Status: capabilities.StatusSupported},

		// Encryption, storage class, metrics
		capabilities.Capability{Operation: "PutTableBucketEncryption", Category: catEncryption,
			Status: capabilities.StatusInert, Notes: "Stored; new tables inherit it. Objects are not encrypted"},
		capabilities.Capability{Operation: "GetTableBucketEncryption", Category: catEncryption,
			Status: capabilities.StatusSupported, Notes: "`AES256` until configured otherwise"},
		capabilities.Capability{Operation: "DeleteTableBucketEncryption", Category: catEncryption,
			Status: capabilities.StatusSupported, Notes: "Returns the bucket to `AES256`"},
		capabilities.Capability{Operation: "GetTableEncryption", Category: catEncryption,
			Status: capabilities.StatusSupported, Notes: "The bucket's configuration at create time, or the table's own"},
		capabilities.Capability{Operation: "PutTableBucketStorageClass", Category: catEncryption,
			Status: capabilities.StatusInert, Notes: "Stored; new tables inherit it"},
		capabilities.Capability{Operation: "GetTableBucketStorageClass", Category: catEncryption,
			Status: capabilities.StatusSupported, Notes: "`STANDARD` until configured otherwise"},
		capabilities.Capability{Operation: "GetTableStorageClass", Category: catEncryption,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "PutTableBucketMetricsConfiguration", Category: catEncryption,
			Status: capabilities.StatusInert, Notes: "Recorded; no CloudWatch metrics are published"},
		capabilities.Capability{Operation: "GetTableBucketMetricsConfiguration", Category: catEncryption,
			Status: capabilities.StatusSupported, Notes: "`NotFoundException` until configured"},
		capabilities.Capability{Operation: "DeleteTableBucketMetricsConfiguration", Category: catEncryption,
			Status: capabilities.StatusSupported},

		// Maintenance
		capabilities.Capability{Operation: "PutTableBucketMaintenanceConfiguration", Category: catMaintenance,
			Status: capabilities.StatusInert, Notes: "Stored and echoed; no maintenance runs"},
		capabilities.Capability{Operation: "GetTableBucketMaintenanceConfiguration", Category: catMaintenance,
			Status: capabilities.StatusSupported, Notes: "AWS's defaults merged with what was put"},
		capabilities.Capability{Operation: "PutTableMaintenanceConfiguration", Category: catMaintenance,
			Status: capabilities.StatusInert, Notes: "Stored and echoed; no compaction or snapshot management runs"},
		capabilities.Capability{Operation: "GetTableMaintenanceConfiguration", Category: catMaintenance,
			Status: capabilities.StatusSupported, Notes: "AWS's defaults merged with what was put"},
		capabilities.Capability{Operation: "GetTableMaintenanceJobStatus", Category: catMaintenance,
			Status: capabilities.StatusInert, Notes: "Every job reports `Not_Yet_Run`, or `Disabled` when turned off"},

		// Replication and record expiration
		capabilities.Capability{Operation: "PutTableBucketReplication", Category: catReplication,
			Status: capabilities.StatusInert, Notes: "Stored with a `versionToken`; nothing is replicated"},
		capabilities.Capability{Operation: "GetTableBucketReplication", Category: catReplication,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "DeleteTableBucketReplication", Category: catReplication,
			Status: capabilities.StatusSupported, Notes: "Optional `versionToken` check"},
		capabilities.Capability{Operation: "PutTableReplication", Category: catReplication,
			Status: capabilities.StatusInert, Notes: "Stored with a `versionToken`; nothing is replicated"},
		capabilities.Capability{Operation: "GetTableReplication", Category: catReplication,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "DeleteTableReplication", Category: catReplication,
			Status: capabilities.StatusSupported, Notes: "`versionToken` check"},
		capabilities.Capability{Operation: "GetTableReplicationStatus", Category: catReplication,
			Status: capabilities.StatusInert, Notes: "Every destination reports `pending`"},
		capabilities.Capability{Operation: "PutTableRecordExpirationConfiguration", Category: catReplication,
			Status: capabilities.StatusInert, Notes: "Stored and echoed; no records expire"},
		capabilities.Capability{Operation: "GetTableRecordExpirationConfiguration", Category: catReplication,
			Status: capabilities.StatusSupported, Notes: "`disabled` until configured"},
		capabilities.Capability{Operation: "GetTableRecordExpirationJobStatus", Category: catReplication,
			Status: capabilities.StatusInert, Notes: "`NotYetRun` when enabled, otherwise `Disabled`"},

		// Tags
		capabilities.Capability{Operation: "TagResource", Category: catTags,
			Status: capabilities.StatusSupported, Notes: "Table buckets and tables; the 50-tag limit and AWS's key and value rules"},
		capabilities.Capability{Operation: "UntagResource", Category: catTags,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "ListTagsForResource", Category: catTags,
			Status: capabilities.StatusSupported},

		// Iceberg REST catalog, served at /iceberg and named by the spec's
		// operationIds. EmulatorOnly: the Iceberg REST spec defines these, not
		// the S3 Tables API model, so they are documented here but never
		// counted as AWS operations the service covers.
		capabilities.Capability{Operation: "IcebergGetConfig", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Needs `warehouse`; the `prefix` override is the URL-encoded ARN, and the defaults point the client's FileIO at Overcast's S3"},
		capabilities.Capability{Operation: "IcebergListNamespaces", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "`pageToken`/`pageSize`; a `parent` namespace has no children, since S3 Tables namespaces have one level"},
		capabilities.Capability{Operation: "IcebergCreateNamespace", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "One-level namespaces; any properties are kept, where AWS supports only `owner`"},
		capabilities.Capability{Operation: "IcebergLoadNamespaceMetadata", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "IcebergNamespaceExists", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "`HEAD`"},
		capabilities.Capability{Operation: "IcebergDropNamespace", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "`NamespaceNotEmptyException` while the namespace still holds tables"},
		capabilities.Capability{Operation: "IcebergUpdateProperties", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Not served by AWS; `UnprocessableEntityException` for a key both set and removed"},
		capabilities.Capability{Operation: "IcebergListTables", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "`pageToken`/`pageSize`"},
		capabilities.Capability{Operation: "IcebergCreateTable", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Writes the first metadata file; also accepts `stage-create`, which AWS refuses with 400"},
		capabilities.Capability{Operation: "IcebergRegisterTable", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Not served by AWS; the metadata's location must be an unused `--table-s3` warehouse"},
		capabilities.Capability{Operation: "IcebergLoadTable", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Reads the table's current metadata file from its warehouse"},
		capabilities.Capability{Operation: "IcebergTableExists", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "`HEAD`"},
		capabilities.Capability{Operation: "IcebergUpdateTable", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Every format v1/v2 update and requirement, in `UpdateTableMetadataLocation`'s compare-and-swap; `CommitFailedException` on a conflict"},
		capabilities.Capability{Operation: "IcebergDropTable", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Only with `purgeRequested=true`, as on AWS; the files stay in the warehouse"},
		capabilities.Capability{Operation: "IcebergRenameTable", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Within the table bucket"},
		capabilities.Capability{Operation: "IcebergReportMetrics", Category: catIceberg, EmulatorOnly: true,
			Status: capabilities.StatusSupported, Notes: "Not served by AWS; accepted and dropped"},
	)
}
