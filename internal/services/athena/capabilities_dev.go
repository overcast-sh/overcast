//go:build dev

package athena

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	const svc = "athena"
	capabilities.Default.Register(
		// Queries
		capabilities.Capability{Service: svc, Operation: "StartQueryExecution", Category: "Queries", Status: capabilities.StatusSupported,
			Notes: "Runs on a Trino engine container started on the first query; Hive DDL runs against the Glue Data Catalog; results written to OutputLocation. Without Docker, or with ATHENA_ENGINE=inert, queries succeed with no rows"},
		capabilities.Capability{Service: svc, Operation: "GetQueryExecution", Category: "Queries", Status: capabilities.StatusSupported,
			Notes: "Full QueryExecution: context, statement type, engine version, resolved result configuration, statistics"},
		capabilities.Capability{Service: svc, Operation: "BatchGetQueryExecution", Category: "Queries", Status: capabilities.StatusSupported,
			Notes: "Unknown IDs come back as UnprocessedQueryExecutionIds"},
		capabilities.Capability{Service: svc, Operation: "ListQueryExecutions", Category: "Queries", Status: capabilities.StatusSupported,
			Notes: "One workgroup (primary by default), most recent first, paginated"},
		capabilities.Capability{Service: svc, Operation: "StopQueryExecution", Category: "Queries", Status: capabilities.StatusSupported,
			Notes: "Cancels an unfinished query on the engine; a finished one keeps its state"},
		capabilities.Capability{Service: svc, Operation: "GetQueryResults", Category: "Queries", Status: capabilities.StatusSupported,
			Notes: "Paginated; a SELECT's header row first, Athena ColumnInfo types, UpdateCount for DML; an unfinished or failed query is InvalidRequestException"},
		capabilities.Capability{Service: svc, Operation: "GetQueryRuntimeStatistics", Category: "Queries", Status: capabilities.StatusPartial,
			Notes: "Timeline and Rows from the engine's statistics; OutputStage is not reported"},

		// Workgroups
		capabilities.Capability{Service: svc, Operation: "CreateWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported,
			Notes: "Duplicate names are InvalidRequestException; engine version resolved"},
		capabilities.Capability{Service: svc, Operation: "GetWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported,
			Notes: "Includes the built-in primary workgroup"},
		capabilities.Capability{Service: svc, Operation: "ListWorkGroups", Category: "WorkGroups", Status: capabilities.StatusSupported,
			Notes: "Full summaries, paginated"},
		capabilities.Capability{Service: svc, Operation: "UpdateWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported,
			Notes: "Description, State and ConfigurationUpdates, including the Remove* flags"},
		capabilities.Capability{Service: svc, Operation: "DeleteWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported,
			Notes: "primary cannot be deleted; a workgroup with named queries or prepared statements needs RecursiveDeleteOption"},
		capabilities.Capability{Service: svc, Operation: "ListEngineVersions", Category: "WorkGroups", Status: capabilities.StatusSupported,
			Notes: "AUTO and Athena engine version 3"},

		// Named queries
		capabilities.Capability{Service: svc, Operation: "CreateNamedQuery", Category: "NamedQueries", Status: capabilities.StatusSupported,
			Notes: "Honours ClientRequestToken"},
		capabilities.Capability{Service: svc, Operation: "GetNamedQuery", Category: "NamedQueries", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "BatchGetNamedQuery", Category: "NamedQueries", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "ListNamedQueries", Category: "NamedQueries", Status: capabilities.StatusSupported,
			Notes: "One workgroup (primary by default), paginated"},
		capabilities.Capability{Service: svc, Operation: "UpdateNamedQuery", Category: "NamedQueries", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "DeleteNamedQuery", Category: "NamedQueries", Status: capabilities.StatusSupported},

		// Prepared statements
		capabilities.Capability{Service: svc, Operation: "CreatePreparedStatement", Category: "PreparedStatements", Status: capabilities.StatusSupported,
			Notes: "EXECUTE ... USING runs the statement on the engine"},
		capabilities.Capability{Service: svc, Operation: "GetPreparedStatement", Category: "PreparedStatements", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "BatchGetPreparedStatement", Category: "PreparedStatements", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "ListPreparedStatements", Category: "PreparedStatements", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "UpdatePreparedStatement", Category: "PreparedStatements", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: svc, Operation: "DeletePreparedStatement", Category: "PreparedStatements", Status: capabilities.StatusSupported},

		// Data catalogs and metadata
		capabilities.Capability{Service: svc, Operation: "CreateDataCatalog", Category: "DataCatalogs", Status: capabilities.StatusPartial,
			Notes: "GLUE, LAMBDA and HIVE are registered; FEDERATED is not emulated"},
		capabilities.Capability{Service: svc, Operation: "GetDataCatalog", Category: "DataCatalogs", Status: capabilities.StatusSupported,
			Notes: "Includes the built-in AwsDataCatalog"},
		capabilities.Capability{Service: svc, Operation: "ListDataCatalogs", Category: "DataCatalogs", Status: capabilities.StatusSupported,
			Notes: "As on AWS, s3tablescatalog/<bucket> catalogs are not listed unless registered"},
		capabilities.Capability{Service: svc, Operation: "UpdateDataCatalog", Category: "DataCatalogs", Status: capabilities.StatusSupported,
			Notes: "AwsDataCatalog cannot be modified"},
		capabilities.Capability{Service: svc, Operation: "DeleteDataCatalog", Category: "DataCatalogs", Status: capabilities.StatusSupported,
			Notes: "AwsDataCatalog cannot be deleted"},
		capabilities.Capability{Service: svc, Operation: "GetDatabase", Category: "DataCatalogs", Status: capabilities.StatusPartial,
			Notes: "Reads the Glue Data Catalog, s3tablescatalog/<bucket> included; LAMBDA and HIVE catalogs are not readable"},
		capabilities.Capability{Service: svc, Operation: "ListDatabases", Category: "DataCatalogs", Status: capabilities.StatusPartial,
			Notes: "Reads the Glue Data Catalog, s3tablescatalog/<bucket> included; paginated"},
		capabilities.Capability{Service: svc, Operation: "GetTableMetadata", Category: "DataCatalogs", Status: capabilities.StatusPartial,
			Notes: "Reads the Glue Data Catalog, s3tablescatalog/<bucket> included"},
		capabilities.Capability{Service: svc, Operation: "ListTableMetadata", Category: "DataCatalogs", Status: capabilities.StatusPartial,
			Notes: "Reads the Glue Data Catalog, s3tablescatalog/<bucket> included; Expression is a name regex; paginated"},

		// Tags
		capabilities.Capability{Service: svc, Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported,
			Notes: "Workgroups and data catalogs"},
		capabilities.Capability{Service: svc, Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported,
			Notes: "Workgroups and data catalogs"},
		capabilities.Capability{Service: svc, Operation: "ListTagsForResource", Category: "Tags", Status: capabilities.StatusSupported,
			Notes: "Workgroups and data catalogs"},
	)
}
