package events

// Data-lake events: Athena query executions, Glue Data Catalog tables and
// S3 Tables tables. They let the console and the system map follow a query or
// a catalog change as it happens instead of polling for it.

const (
	// AthenaQueryStateChanged fires when a query execution is stored QUEUED
	// by StartQueryExecution, and again at each later state it reaches:
	// RUNNING, then one of SUCCEEDED, FAILED or CANCELLED. A report that
	// does not move the execution forward publishes nothing.
	// Payload: AthenaQueryStatePayload.
	AthenaQueryStateChanged Type = "athena:QueryStateChanged"

	// GlueTableChanged fires after a Data Catalog table is created, updated
	// or deleted, whether through the Glue API, Athena DDL or CloudFormation.
	// DeleteDatabase publishes a delete for each table it removed.
	// Payload: GlueTablePayload, with Change set.
	GlueTableChanged Type = "glue:TableChanged"
	// GluePartitionsChanged fires once per Glue request that creates, updates
	// or deletes at least one partition of a table, however many it touched.
	// Athena DDL writes partitions in batches of 100, so a statement that
	// touches more publishes once per batch. Payload: GlueTablePayload, with
	// Change empty.
	GluePartitionsChanged Type = "glue:PartitionsChanged"

	// S3TablesTableCreated fires after an S3 Tables table is created: by
	// CreateTable, by the Iceberg REST catalog's create or register, or by the
	// commit that completes a staged create. A table created with its first
	// metadata file by CreateTable or the catalog's create publishes only
	// this; a register or staged create, which adopt a file, also publish
	// S3TablesTableCommitted. Payload: S3TablesTablePayload.
	S3TablesTableCreated Type = "s3tables:TableCreated"
	// S3TablesTableDeleted fires after an S3 Tables table is deleted, by
	// DeleteTable or the Iceberg REST catalog's drop.
	// Payload: S3TablesTablePayload.
	S3TablesTableDeleted Type = "s3tables:TableDeleted"
	// S3TablesTableRenamed fires after RenameTable, or the Iceberg REST
	// catalog's rename, moves a table to a new name or namespace. Its ARN is
	// unchanged. Payload: S3TablesTablePayload, naming where the table is now.
	S3TablesTableRenamed Type = "s3tables:TableRenamed"
	// S3TablesTableCommitted fires after a table is pointed at a new metadata
	// file: an Iceberg REST commit or register, or UpdateTableMetadataLocation.
	// It is published after the commit's lock is released, so two racing
	// commits' events can arrive in either order: follow
	// PreviousMetadataLocation, not arrival order. Payload: S3TablesCommitPayload.
	S3TablesTableCommitted Type = "s3tables:TableCommitted"
)

// AthenaQueryStatePayload carries one state an Athena query execution
// reached. Catalog is its QueryExecutionContext catalog, empty when it named
// none (AwsDataCatalog). Database is the one the query's unqualified names
// resolve in: its QueryExecutionContext database, or "default". Tables lists
// the table a DDL statement names, qualified as "database.table"; the tables
// of a statement the engine runs are not parsed out, so it is empty for them.
type AthenaQueryStatePayload struct {
	QueryExecutionID string   `json:"queryExecutionId"`
	WorkGroup        string   `json:"workGroup"`
	State            string   `json:"state"`
	Catalog          string   `json:"catalog,omitempty"`
	Database         string   `json:"database"`
	Tables           []string `json:"tables,omitempty"`
}

// What a GlueTableChanged event did to its table.
const (
	GlueTableCreated = "created"
	GlueTableUpdated = "updated"
	GlueTableDeleted = "deleted"
)

// GlueTablePayload names the Data Catalog table a Glue event is about.
// Change is one of GlueTableCreated, GlueTableUpdated and GlueTableDeleted
// on GlueTableChanged, and empty on GluePartitionsChanged.
type GlueTablePayload struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	ARN      string `json:"arn,omitempty"`
	Change   string `json:"change,omitempty"`
}

// arnFromPayload implements arnCarrier for GlueTablePayload.
func (p GlueTablePayload) arnFromPayload() string { return p.ARN }

// S3TablesTablePayload names an S3 Tables table: its ARN, which carries the
// table's id and survives a rename, and where it is now.
type S3TablesTablePayload struct {
	ARN       string `json:"arn"`
	Bucket    string `json:"bucket"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// arnFromPayload implements arnCarrier for S3TablesTablePayload, and for
// S3TablesCommitPayload, which embeds it.
func (p S3TablesTablePayload) arnFromPayload() string { return p.ARN }

// S3TablesCommitPayload carries one move of a table's metadata pointer.
// PreviousMetadataLocation is empty for a table's first metadata file.
// SnapshotID, Operation and AddedRecords describe the new metadata's current
// snapshot, when it has one and the file could be read; AddedRecords is set
// only when the snapshot summary records it. SnapshotID is a decimal string
// because Iceberg's 64-bit ids do not survive a JavaScript number.
type S3TablesCommitPayload struct {
	S3TablesTablePayload
	PreviousMetadataLocation string `json:"previousMetadataLocation,omitempty"`
	MetadataLocation         string `json:"metadataLocation"`
	SnapshotID               string `json:"snapshotId,omitempty"`
	Operation                string `json:"operation,omitempty"`
	AddedRecords             *int64 `json:"addedRecords,omitempty"`
}
