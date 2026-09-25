package athena

// model.go — Athena's wire shapes.
//
// Each struct is named after its Athena model structure
// (internal/awsshapes/tables/athena.txt) and carries the members Overcast
// reports, spelled as the model spells them. Optional booleans and numbers are
// pointers so that a member a caller never sent stays absent on the way back
// rather than coming back as a zero it did not choose.
//
// Members Overcast only stores and echoes, and whose inner shape it never
// reads (Spark engine settings, monitoring, Identity Center, S3 Access
// Grants, customer-content encryption), are kept as decoded JSON values: they
// round-trip exactly and cannot drift from the model.
//
// Timestamps are epoch seconds, the AWS JSON 1.1 encoding.

// ─── Workgroups ───────────────────────────────────────────────

// AclConfiguration is the S3 ACL applied to query results.
type AclConfiguration struct {
	S3AclOption string `json:"S3AclOption"`
}

// EncryptionConfiguration is how query results are encrypted.
type EncryptionConfiguration struct {
	EncryptionOption string `json:"EncryptionOption"`
	KmsKey           string `json:"KmsKey,omitempty"`
}

// ResultConfiguration is where and how query results are written.
type ResultConfiguration struct {
	AclConfiguration        *AclConfiguration        `json:"AclConfiguration,omitempty"`
	EncryptionConfiguration *EncryptionConfiguration `json:"EncryptionConfiguration,omitempty"`
	ExpectedBucketOwner     string                   `json:"ExpectedBucketOwner,omitempty"`
	OutputLocation          string                   `json:"OutputLocation,omitempty"`
}

// ResultConfigurationUpdates is UpdateWorkGroup's edit of a ResultConfiguration.
type ResultConfigurationUpdates struct {
	ResultConfiguration
	RemoveAclConfiguration        *bool `json:"RemoveAclConfiguration,omitempty"`
	RemoveEncryptionConfiguration *bool `json:"RemoveEncryptionConfiguration,omitempty"`
	RemoveExpectedBucketOwner     *bool `json:"RemoveExpectedBucketOwner,omitempty"`
	RemoveOutputLocation          *bool `json:"RemoveOutputLocation,omitempty"`
}

// EngineVersion names the engine a workgroup's queries run on.
type EngineVersion struct {
	EffectiveEngineVersion string `json:"EffectiveEngineVersion,omitempty"`
	SelectedEngineVersion  string `json:"SelectedEngineVersion,omitempty"`
}

// ManagedQueryResultsConfiguration keeps results in Athena-owned storage.
type ManagedQueryResultsConfiguration struct {
	Enabled                 bool `json:"Enabled"`
	EncryptionConfiguration any  `json:"EncryptionConfiguration,omitempty"`
}

// ManagedQueryResultsConfigurationUpdates is UpdateWorkGroup's edit of it.
type ManagedQueryResultsConfigurationUpdates struct {
	Enabled                       *bool `json:"Enabled,omitempty"`
	EncryptionConfiguration       any   `json:"EncryptionConfiguration,omitempty"`
	RemoveEncryptionConfiguration *bool `json:"RemoveEncryptionConfiguration,omitempty"`
}

// WorkGroupConfiguration is a workgroup's settings.
type WorkGroupConfiguration struct {
	AdditionalConfiguration                 string                            `json:"AdditionalConfiguration,omitempty"`
	BytesScannedCutoffPerQuery              *int64                            `json:"BytesScannedCutoffPerQuery,omitempty"`
	CustomerContentEncryptionConfiguration  any                               `json:"CustomerContentEncryptionConfiguration,omitempty"`
	EnableMinimumEncryptionConfiguration    *bool                             `json:"EnableMinimumEncryptionConfiguration,omitempty"`
	EnforceWorkGroupConfiguration           *bool                             `json:"EnforceWorkGroupConfiguration,omitempty"`
	EngineConfiguration                     any                               `json:"EngineConfiguration,omitempty"`
	EngineVersion                           *EngineVersion                    `json:"EngineVersion,omitempty"`
	ExecutionRole                           string                            `json:"ExecutionRole,omitempty"`
	IdentityCenterConfiguration             any                               `json:"IdentityCenterConfiguration,omitempty"`
	ManagedQueryResultsConfiguration        *ManagedQueryResultsConfiguration `json:"ManagedQueryResultsConfiguration,omitempty"`
	MonitoringConfiguration                 any                               `json:"MonitoringConfiguration,omitempty"`
	PublishCloudWatchMetricsEnabled         *bool                             `json:"PublishCloudWatchMetricsEnabled,omitempty"`
	QueryResultsS3AccessGrantsConfiguration any                               `json:"QueryResultsS3AccessGrantsConfiguration,omitempty"`
	RequesterPaysEnabled                    *bool                             `json:"RequesterPaysEnabled,omitempty"`
	ResultConfiguration                     *ResultConfiguration              `json:"ResultConfiguration,omitempty"`
}

// WorkGroupConfigurationUpdates is UpdateWorkGroup's edit of a configuration.
type WorkGroupConfigurationUpdates struct {
	AdditionalConfiguration                      string                                   `json:"AdditionalConfiguration,omitempty"`
	BytesScannedCutoffPerQuery                   *int64                                   `json:"BytesScannedCutoffPerQuery,omitempty"`
	CustomerContentEncryptionConfiguration       any                                      `json:"CustomerContentEncryptionConfiguration,omitempty"`
	EnableMinimumEncryptionConfiguration         *bool                                    `json:"EnableMinimumEncryptionConfiguration,omitempty"`
	EnforceWorkGroupConfiguration                *bool                                    `json:"EnforceWorkGroupConfiguration,omitempty"`
	EngineConfiguration                          any                                      `json:"EngineConfiguration,omitempty"`
	EngineVersion                                *EngineVersion                           `json:"EngineVersion,omitempty"`
	ExecutionRole                                string                                   `json:"ExecutionRole,omitempty"`
	ManagedQueryResultsConfigurationUpdates      *ManagedQueryResultsConfigurationUpdates `json:"ManagedQueryResultsConfigurationUpdates,omitempty"`
	MonitoringConfiguration                      any                                      `json:"MonitoringConfiguration,omitempty"`
	PublishCloudWatchMetricsEnabled              *bool                                    `json:"PublishCloudWatchMetricsEnabled,omitempty"`
	QueryResultsS3AccessGrantsConfiguration      any                                      `json:"QueryResultsS3AccessGrantsConfiguration,omitempty"`
	RemoveBytesScannedCutoffPerQuery             *bool                                    `json:"RemoveBytesScannedCutoffPerQuery,omitempty"`
	RemoveCustomerContentEncryptionConfiguration *bool                                    `json:"RemoveCustomerContentEncryptionConfiguration,omitempty"`
	RequesterPaysEnabled                         *bool                                    `json:"RequesterPaysEnabled,omitempty"`
	ResultConfigurationUpdates                   *ResultConfigurationUpdates              `json:"ResultConfigurationUpdates,omitempty"`
}

// WorkGroup is the wire shape GetWorkGroup returns. The AWS model's WorkGroup
// carries no Tags member, so tags are never embedded here; see
// workGroupRecord.
type WorkGroup struct {
	Configuration *WorkGroupConfiguration `json:"Configuration,omitempty"`
	CreationTime  float64                 `json:"CreationTime,omitempty"`
	Description   string                  `json:"Description,omitempty"`
	Name          string                  `json:"Name"`
	State         string                  `json:"State"`
}

// WorkGroupSummary is one entry of ListWorkGroups.
type WorkGroupSummary struct {
	CreationTime  float64        `json:"CreationTime,omitempty"`
	Description   string         `json:"Description,omitempty"`
	EngineVersion *EngineVersion `json:"EngineVersion,omitempty"`
	Name          string         `json:"Name"`
	State         string         `json:"State"`
}

// ─── Query executions ─────────────────────────────────────────

// QueryExecutionContext is the catalog and database a query runs in.
type QueryExecutionContext struct {
	Catalog  string `json:"Catalog,omitempty"`
	Database string `json:"Database,omitempty"`
}

// ResultReuseByAgeConfiguration reuses a previous result up to an age.
type ResultReuseByAgeConfiguration struct {
	Enabled         bool   `json:"Enabled"`
	MaxAgeInMinutes *int32 `json:"MaxAgeInMinutes,omitempty"`
}

// ResultReuseConfiguration is a query's result-reuse behaviour.
type ResultReuseConfiguration struct {
	ResultReuseByAgeConfiguration *ResultReuseByAgeConfiguration `json:"ResultReuseByAgeConfiguration,omitempty"`
}

// ResultReuseInformation reports whether a previous result was reused.
type ResultReuseInformation struct {
	ReusedPreviousResult bool `json:"ReusedPreviousResult"`
}

// QueryExecutionStatistics are the timings and volumes of one execution.
type QueryExecutionStatistics struct {
	DataManifestLocation             string                  `json:"DataManifestLocation,omitempty"`
	DataScannedInBytes               int64                   `json:"DataScannedInBytes"`
	EngineExecutionTimeInMillis      int64                   `json:"EngineExecutionTimeInMillis"`
	QueryPlanningTimeInMillis        int64                   `json:"QueryPlanningTimeInMillis"`
	QueryQueueTimeInMillis           int64                   `json:"QueryQueueTimeInMillis"`
	ResultReuseInformation           *ResultReuseInformation `json:"ResultReuseInformation,omitempty"`
	ServicePreProcessingTimeInMillis int64                   `json:"ServicePreProcessingTimeInMillis"`
	ServiceProcessingTimeInMillis    int64                   `json:"ServiceProcessingTimeInMillis"`
	TotalExecutionTimeInMillis       int64                   `json:"TotalExecutionTimeInMillis"`
}

// AthenaError is why a query execution failed.
//
//nolint:revive // AWS names the shape AthenaError.
type AthenaError struct {
	ErrorCategory int32  `json:"ErrorCategory,omitempty"`
	ErrorMessage  string `json:"ErrorMessage,omitempty"`
	ErrorType     int32  `json:"ErrorType,omitempty"`
	Retryable     bool   `json:"Retryable"`
}

// QueryExecutionStatus is where an execution is in its lifecycle.
type QueryExecutionStatus struct {
	AthenaError        *AthenaError `json:"AthenaError,omitempty"`
	CompletionDateTime float64      `json:"CompletionDateTime,omitempty"`
	State              string       `json:"State"`
	StateChangeReason  string       `json:"StateChangeReason,omitempty"`
	SubmissionDateTime float64      `json:"SubmissionDateTime"`
}

// QueryExecution is one run of a query, as GetQueryExecution returns it.
type QueryExecution struct {
	EngineVersion       *EngineVersion `json:"EngineVersion,omitempty"`
	ExecutionParameters []string       `json:"ExecutionParameters,omitempty"`
	// ManagedQueryResultsConfiguration and QueryResultsS3AccessGrantsConfiguration
	// are the workgroup's, as the query ran under them.
	ManagedQueryResultsConfiguration        *ManagedQueryResultsConfiguration `json:"ManagedQueryResultsConfiguration,omitempty"`
	QueryResultsS3AccessGrantsConfiguration any                               `json:"QueryResultsS3AccessGrantsConfiguration,omitempty"`
	Query                                   string                            `json:"Query"`
	QueryExecutionContext                   QueryExecutionContext             `json:"QueryExecutionContext"`
	QueryExecutionId                        string                            `json:"QueryExecutionId"`
	ResultConfiguration                     ResultConfiguration               `json:"ResultConfiguration"`
	ResultReuseConfiguration                *ResultReuseConfiguration         `json:"ResultReuseConfiguration,omitempty"`
	StatementType                           string                            `json:"StatementType,omitempty"`
	Statistics                              *QueryExecutionStatistics         `json:"Statistics,omitempty"`
	Status                                  QueryExecutionStatus              `json:"Status"`
	WorkGroup                               string                            `json:"WorkGroup"`
}

// ─── Named queries and prepared statements ────────────────────

// NamedQuery is a saved query.
type NamedQuery struct {
	Database     string `json:"Database"`
	Description  string `json:"Description,omitempty"`
	Name         string `json:"Name"`
	NamedQueryId string `json:"NamedQueryId"`
	QueryString  string `json:"QueryString"`
	WorkGroup    string `json:"WorkGroup"`
}

// PreparedStatement is a named, parameterised query in a workgroup.
type PreparedStatement struct {
	Description      string  `json:"Description,omitempty"`
	LastModifiedTime float64 `json:"LastModifiedTime"`
	QueryStatement   string  `json:"QueryStatement"`
	StatementName    string  `json:"StatementName"`
	WorkGroupName    string  `json:"WorkGroupName"`
}

// PreparedStatementSummary is one entry of ListPreparedStatements.
type PreparedStatementSummary struct {
	LastModifiedTime float64 `json:"LastModifiedTime"`
	StatementName    string  `json:"StatementName"`
}

// ─── Data catalogs and metadata ───────────────────────────────

// DataCatalog is a registered catalog. Tags are kept off it for the same
// reason as WorkGroup's; see dataCatalogRecord.
type DataCatalog struct {
	Description string            `json:"Description,omitempty"`
	Name        string            `json:"Name"`
	Parameters  map[string]string `json:"Parameters,omitempty"`
	Status      string            `json:"Status,omitempty"`
	Type        string            `json:"Type"`
}

// DataCatalogSummary is one entry of ListDataCatalogs.
type DataCatalogSummary struct {
	CatalogName string `json:"CatalogName"`
	Status      string `json:"Status,omitempty"`
	Type        string `json:"Type"`
}

// Database is a database in a catalog.
type Database struct {
	Description string            `json:"Description,omitempty"`
	Name        string            `json:"Name"`
	Parameters  map[string]string `json:"Parameters,omitempty"`
}

// Column is a table column or partition key.
type Column struct {
	Comment string `json:"Comment,omitempty"`
	Name    string `json:"Name"`
	Type    string `json:"Type,omitempty"`
}

// TableMetadata is a table's schema and properties.
type TableMetadata struct {
	Columns        []Column          `json:"Columns"`
	CreateTime     float64           `json:"CreateTime,omitempty"`
	LastAccessTime float64           `json:"LastAccessTime,omitempty"`
	Name           string            `json:"Name"`
	Parameters     map[string]string `json:"Parameters,omitempty"`
	PartitionKeys  []Column          `json:"PartitionKeys"`
	TableType      string            `json:"TableType,omitempty"`
}

// ─── Batch failures ───────────────────────────────────────────

// UnprocessedNamedQueryId is a BatchGetNamedQuery miss.
type UnprocessedNamedQueryId struct {
	ErrorCode    string `json:"ErrorCode,omitempty"`
	ErrorMessage string `json:"ErrorMessage,omitempty"`
	NamedQueryId string `json:"NamedQueryId"`
}

// UnprocessedQueryExecutionId is a BatchGetQueryExecution miss.
type UnprocessedQueryExecutionId struct {
	ErrorCode        string `json:"ErrorCode,omitempty"`
	ErrorMessage     string `json:"ErrorMessage,omitempty"`
	QueryExecutionId string `json:"QueryExecutionId"`
}

// UnprocessedPreparedStatementName is a BatchGetPreparedStatement miss.
type UnprocessedPreparedStatementName struct {
	ErrorCode     string `json:"ErrorCode,omitempty"`
	ErrorMessage  string `json:"ErrorMessage,omitempty"`
	StatementName string `json:"StatementName"`
}

// ─── Query results ────────────────────────────────────────────

// Datum is one cell of a result row.
type Datum struct {
	VarCharValue *string `json:"VarCharValue,omitempty"`
}

// Row is one result row.
type Row struct {
	Data []Datum `json:"Data"`
}

// ColumnInfo describes one result column.
type ColumnInfo struct {
	CaseSensitive bool   `json:"CaseSensitive"`
	CatalogName   string `json:"CatalogName,omitempty"`
	Label         string `json:"Label,omitempty"`
	Name          string `json:"Name"`
	Nullable      string `json:"Nullable,omitempty"`
	Precision     int32  `json:"Precision"`
	Scale         int32  `json:"Scale"`
	SchemaName    string `json:"SchemaName,omitempty"`
	TableName     string `json:"TableName,omitempty"`
	Type          string `json:"Type"`
}

// ResultSetMetadata describes a result's columns.
type ResultSetMetadata struct {
	ColumnInfo []ColumnInfo `json:"ColumnInfo"`
}

// ResultSet is a page of a query's results.
type ResultSet struct {
	ResultSetMetadata ResultSetMetadata `json:"ResultSetMetadata"`
	Rows              []Row             `json:"Rows"`
}

// ─── Runtime statistics ───────────────────────────────────────

// QueryRuntimeStatisticsTimeline is where an execution's time went.
type QueryRuntimeStatisticsTimeline struct {
	EngineExecutionTimeInMillis      int64 `json:"EngineExecutionTimeInMillis"`
	QueryPlanningTimeInMillis        int64 `json:"QueryPlanningTimeInMillis"`
	QueryQueueTimeInMillis           int64 `json:"QueryQueueTimeInMillis"`
	ServicePreProcessingTimeInMillis int64 `json:"ServicePreProcessingTimeInMillis"`
	ServiceProcessingTimeInMillis    int64 `json:"ServiceProcessingTimeInMillis"`
	TotalExecutionTimeInMillis       int64 `json:"TotalExecutionTimeInMillis"`
}

// QueryRuntimeStatisticsRows is what an execution read and produced.
type QueryRuntimeStatisticsRows struct {
	InputBytes  int64 `json:"InputBytes"`
	InputRows   int64 `json:"InputRows"`
	OutputBytes int64 `json:"OutputBytes"`
	OutputRows  int64 `json:"OutputRows"`
}

// QueryRuntimeStatistics is GetQueryRuntimeStatistics' report. OutputStage,
// the per-stage plan tree, is not reported.
type QueryRuntimeStatistics struct {
	Rows     *QueryRuntimeStatisticsRows     `json:"Rows,omitempty"`
	Timeline *QueryRuntimeStatisticsTimeline `json:"Timeline,omitempty"`
}
