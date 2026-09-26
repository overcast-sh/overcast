//go:build dev

package glue

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	const svc = "glue"
	capabilities.Default.Register(
		// Catalogs
		capabilities.Capability{Service: svc, Operation: "GetCatalog", Category: "Catalogs", Status: capabilities.StatusSupported,
			Notes: "The account's catalog, s3tablescatalog and one child per S3 Tables table bucket"},
		capabilities.Capability{Service: svc, Operation: "GetCatalogs", Category: "Catalogs", Status: capabilities.StatusPartial,
			Notes: "ParentCatalogId, IncludeRoot and Recursive; HasDatabases is not applied"},

		// Databases
		capabilities.Capability{Service: svc, Operation: "CreateDatabase", Category: "Databases", Status: capabilities.StatusSupported,
			Notes: "Keeps the whole DatabaseInput and Tags; duplicate names are AlreadyExistsException"},
		capabilities.Capability{Service: svc, Operation: "GetDatabase", Category: "Databases", Status: capabilities.StatusSupported,
			Notes: "Returns the full database with CreateTime; an S3 Tables namespace in s3tablescatalog/<bucket>"},
		capabilities.Capability{Service: svc, Operation: "GetDatabases", Category: "Databases", Status: capabilities.StatusSupported,
			Notes: "Paginated; s3tablescatalog/<bucket> lists the bucket's namespaces"},
		capabilities.Capability{Service: svc, Operation: "UpdateDatabase", Category: "Databases", Status: capabilities.StatusSupported,
			Notes: "Replaces the definition; renaming is refused"},
		capabilities.Capability{Service: svc, Operation: "DeleteDatabase", Category: "Databases", Status: capabilities.StatusSupported,
			Notes: "Also deletes the database's tables, partitions and table versions"},

		// Tables
		capabilities.Capability{Service: svc, Operation: "CreateTable", Category: "Tables", Status: capabilities.StatusPartial,
			Notes: "Keeps the whole TableInput; OpenTableFormatInput.IcebergInput writes no Iceberg metadata"},
		capabilities.Capability{Service: svc, Operation: "GetTable", Category: "Tables", Status: capabilities.StatusSupported,
			Notes: "Returns the full table; an S3 Tables table in s3tablescatalog/<bucket>"},
		capabilities.Capability{Service: svc, Operation: "GetTables", Category: "Tables", Status: capabilities.StatusSupported,
			Notes: "Expression is a name regex; paginated; s3tablescatalog/<bucket> included"},
		capabilities.Capability{Service: svc, Operation: "UpdateTable", Category: "Tables", Status: capabilities.StatusPartial,
			Notes: "VersionId concurrency and archiving; UpdateOpenTableFormatInput is not implemented"},
		capabilities.Capability{Service: svc, Operation: "DeleteTable", Category: "Tables", Status: capabilities.StatusSupported,
			Notes: "Also deletes the table's partitions and versions"},
		capabilities.Capability{Service: svc, Operation: "BatchDeleteTable", Category: "Tables", Status: capabilities.StatusSupported,
			Notes: "Reports missing tables in Errors"},

		// Table versions
		capabilities.Capability{Service: svc, Operation: "GetTableVersion", Category: "Table versions", Status: capabilities.StatusSupported,
			Notes: "Current version when VersionId is omitted"},
		capabilities.Capability{Service: svc, Operation: "GetTableVersions", Category: "Table versions", Status: capabilities.StatusSupported,
			Notes: "Current and archived versions, newest first; paginated"},
		capabilities.Capability{Service: svc, Operation: "DeleteTableVersion", Category: "Table versions", Status: capabilities.StatusSupported,
			Notes: "Archived versions only; the current one is InvalidInputException"},
		capabilities.Capability{Service: svc, Operation: "BatchDeleteTableVersion", Category: "Table versions", Status: capabilities.StatusSupported,
			Notes: "Reports failed versions in Errors"},

		// Partitions
		capabilities.Capability{Service: svc, Operation: "CreatePartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "One value per partition key; duplicates are AlreadyExistsException"},
		capabilities.Capability{Service: svc, Operation: "BatchCreatePartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "Up to 100; per-partition failures in Errors"},
		capabilities.Capability{Service: svc, Operation: "GetPartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "Returns the full partition"},
		capabilities.Capability{Service: svc, Operation: "GetPartitions", Category: "Partitions", Status: capabilities.StatusPartial,
			Notes: "Expression supports comparisons, AND/OR/NOT, IN, BETWEEN, LIKE, IS NULL; paginated, Segment supported"},
		capabilities.Capability{Service: svc, Operation: "BatchGetPartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "Missing partitions are omitted"},
		capabilities.Capability{Service: svc, Operation: "UpdatePartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "Replaces the definition; new Values move the partition"},
		capabilities.Capability{Service: svc, Operation: "DeletePartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "Deletes one partition"},
		capabilities.Capability{Service: svc, Operation: "BatchDeletePartition", Category: "Partitions", Status: capabilities.StatusSupported,
			Notes: "Up to 25; missing partitions in Errors"},

		// Column statistics
		capabilities.Capability{Service: svc, Operation: "UpdateColumnStatisticsForTable", Category: "Column statistics", Status: capabilities.StatusSupported,
			Notes: "Stored and echoed; an unknown column comes back in Errors"},
		capabilities.Capability{Service: svc, Operation: "UpdateColumnStatisticsForPartition", Category: "Column statistics", Status: capabilities.StatusSupported,
			Notes: "Stored and echoed; an unknown column comes back in Errors"},
		capabilities.Capability{Service: svc, Operation: "GetColumnStatisticsForTable", Category: "Column statistics", Status: capabilities.StatusSupported,
			Notes: "A column with no statistics comes back in Errors"},
		capabilities.Capability{Service: svc, Operation: "GetColumnStatisticsForPartition", Category: "Column statistics", Status: capabilities.StatusSupported,
			Notes: "A column with no statistics comes back in Errors"},
		capabilities.Capability{Service: svc, Operation: "DeleteColumnStatisticsForTable", Category: "Column statistics", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "DeleteColumnStatisticsForPartition", Category: "Column statistics", Status: capabilities.StatusSupported},

		// Tags
		capabilities.Capability{Service: svc, Operation: "TagResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Adds or overwrites tags on databases and tables"},
		capabilities.Capability{Service: svc, Operation: "UntagResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Removes tags by key from databases and tables"},
		capabilities.Capability{Service: svc, Operation: "GetTags", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Returns tags for databases and tables"},
	)
}
