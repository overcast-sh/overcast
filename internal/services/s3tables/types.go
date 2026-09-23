package s3tables

// Persisted records and the configuration shapes they carry. Wire member names
// are the model's (s3tables-2018-05-10): lowerCamel, with the Iceberg shapes'
// hyphenated jsonName overrides where the model sets them.

import "time"

// Enumerations from the model.
const (
	sseAES256 = "AES256"
	sseKMS    = "aws:kms"

	storageStandard           = "STANDARD"
	storageIntelligentTiering = "INTELLIGENT_TIERING"

	resourceTypeCustomer = "customer"
	resourceTypeAWS      = "aws"
	formatIceberg        = "ICEBERG"

	statusEnabled  = "enabled"
	statusDisabled = "disabled"

	maintUnreferencedFileRemoval = "icebergUnreferencedFileRemoval"
	maintCompaction              = "icebergCompaction"
	maintSnapshotManagement      = "icebergSnapshotManagement"

	jobNotYetRun    = "Not_Yet_Run"
	jobDisabled     = "Disabled"
	expiryNotYetRun = "NotYetRun"
	expiryDisabled  = "Disabled"

	replicationPending = "pending"
)

type encryptionConfiguration struct {
	SSEAlgorithm string `json:"sseAlgorithm"`
	KMSKeyARN    string `json:"kmsKeyArn,omitempty"`
}

type storageClassConfiguration struct {
	StorageClass string `json:"storageClass"`
}

// ─── Maintenance ──────────────────────────────────────────────────────────────

type unreferencedFileRemovalSettings struct {
	UnreferencedDays *int `json:"unreferencedDays,omitempty"`
	NonCurrentDays   *int `json:"nonCurrentDays,omitempty"`
}

type compactionSettings struct {
	TargetFileSizeMB *int   `json:"targetFileSizeMB,omitempty"`
	Strategy         string `json:"strategy,omitempty"`
}

type snapshotManagementSettings struct {
	MinSnapshotsToKeep  *int `json:"minSnapshotsToKeep,omitempty"`
	MaxSnapshotAgeHours *int `json:"maxSnapshotAgeHours,omitempty"`
}

// maintenanceSettings is the union of TableBucketMaintenanceSettings and
// TableMaintenanceSettings: exactly one member is set, and it must be the one
// the configuration's type names.
type maintenanceSettings struct {
	IcebergUnreferencedFileRemoval *unreferencedFileRemovalSettings `json:"icebergUnreferencedFileRemoval,omitempty"`
	IcebergCompaction              *compactionSettings              `json:"icebergCompaction,omitempty"`
	IcebergSnapshotManagement      *snapshotManagementSettings      `json:"icebergSnapshotManagement,omitempty"`
}

type maintenanceValue struct {
	Status   string               `json:"status,omitempty"`
	Settings *maintenanceSettings `json:"settings,omitempty"`
}

// ─── Record expiration and replication ────────────────────────────────────────

type recordExpirationSettings struct {
	Days *int `json:"days,omitempty"`
}

type recordExpirationValue struct {
	Status   string                    `json:"status,omitempty"`
	Settings *recordExpirationSettings `json:"settings,omitempty"`
}

type replicationDestination struct {
	DestinationTableBucketARN string `json:"destinationTableBucketARN"`
}

type replicationRule struct {
	Destinations []replicationDestination `json:"destinations"`
}

// replicationConfiguration is both TableBucketReplicationConfiguration and
// TableReplicationConfiguration, which the model defines identically.
type replicationConfiguration struct {
	Role  string            `json:"role"`
	Rules []replicationRule `json:"rules"`
}

// replicationState is a stored replication configuration and the token that
// guards changes to it.
type replicationState struct {
	Configuration replicationConfiguration `json:"configuration"`
	VersionToken  string                   `json:"versionToken"`
}

// ─── Records ──────────────────────────────────────────────────────────────────

// tableBucket is a persisted table bucket.
type tableBucket struct {
	Name           string    `json:"name"`
	ARN            string    `json:"arn"`
	Region         string    `json:"region"`
	OwnerAccountID string    `json:"ownerAccountId"`
	TableBucketID  string    `json:"tableBucketId"`
	CreatedAt      time.Time `json:"createdAt"`

	Encryption   encryptionConfiguration `json:"encryption"`
	StorageClass string                  `json:"storageClass"`
	Policy       string                  `json:"policy,omitempty"`
	// MetricsID is the id of the bucket's metrics configuration; empty when
	// none is configured.
	MetricsID   string                      `json:"metricsId,omitempty"`
	Maintenance map[string]maintenanceValue `json:"maintenance,omitempty"`
	Replication *replicationState           `json:"replication,omitempty"`
	Tags        map[string]string           `json:"tags,omitempty"`
}

// namespaceRecord is a persisted namespace.
type namespaceRecord struct {
	Name           string    `json:"name"`
	Bucket         string    `json:"bucket"`
	NamespaceID    string    `json:"namespaceId"`
	CreatedAt      time.Time `json:"createdAt"`
	CreatedBy      string    `json:"createdBy"`
	OwnerAccountID string    `json:"ownerAccountId"`
}

// tableRecord is a persisted table. Its ARN names the table by TableID, which
// RenameTable leaves alone.
type tableRecord struct {
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	NamespaceID       string    `json:"namespaceId"`
	Bucket            string    `json:"bucket"`
	BucketARN         string    `json:"bucketArn"`
	TableBucketID     string    `json:"tableBucketId"`
	Region            string    `json:"region"`
	TableID           string    `json:"tableId"`
	ARN               string    `json:"arn"`
	Format            string    `json:"format"`
	VersionToken      string    `json:"versionToken"`
	MetadataLocation  string    `json:"metadataLocation,omitempty"`
	WarehouseLocation string    `json:"warehouseLocation"`
	CreatedAt         time.Time `json:"createdAt"`
	CreatedBy         string    `json:"createdBy"`
	ModifiedAt        time.Time `json:"modifiedAt"`
	ModifiedBy        string    `json:"modifiedBy"`
	OwnerAccountID    string    `json:"ownerAccountId"`

	Encryption       encryptionConfiguration     `json:"encryption"`
	StorageClass     string                      `json:"storageClass"`
	Policy           string                      `json:"policy,omitempty"`
	Maintenance      map[string]maintenanceValue `json:"maintenance,omitempty"`
	RecordExpiration *recordExpirationValue      `json:"recordExpiration,omitempty"`
	Replication      *replicationState           `json:"replication,omitempty"`
	Tags             map[string]string           `json:"tags,omitempty"`
}
