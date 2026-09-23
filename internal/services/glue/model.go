package glue

// model.go — the Data Catalog's wire shapes.
//
// Every struct here mirrors an AWS Glue model structure member for member
// (internal/awsshapes/tables/glue.txt), because the catalog's job is to hand
// back exactly what a client put. Trino's Glue metastore, Iceberg's and
// PyIceberg's Glue catalogs and CDK's AWS::Glue::Table all read
// StorageDescriptor, Parameters and PartitionKeys back; dropping any of them on
// decode is a table that "exists" and cannot be queried.
//
// Members Overcast only stores and echoes, and whose inner shape it never
// reads (a view's definition, a resource link's target, Lake Formation
// default permissions), are kept as decoded JSON values rather than modelled
// field by field: they round-trip exactly and cannot drift from the model.
//
// Timestamps are epoch seconds, the AWS JSON 1.1 encoding, as every typed
// service here does.

// Column is one column of a table or one of its partition keys.
type Column struct {
	Name       string            `json:"Name"`
	Type       string            `json:"Type,omitempty"`
	Comment    string            `json:"Comment,omitempty"`
	Parameters map[string]string `json:"Parameters,omitempty"`
}

// SerDeInfo names the serializer/deserializer a table's data is read with.
type SerDeInfo struct {
	Name                 string            `json:"Name,omitempty"`
	SerializationLibrary string            `json:"SerializationLibrary,omitempty"`
	Parameters           map[string]string `json:"Parameters,omitempty"`
}

// Order is one sort column of a StorageDescriptor.
type Order struct {
	Column    string `json:"Column"`
	SortOrder int32  `json:"SortOrder"`
}

// SkewedInfo describes skewed column values.
type SkewedInfo struct {
	SkewedColumnNames             []string          `json:"SkewedColumnNames,omitempty"`
	SkewedColumnValues            []string          `json:"SkewedColumnValues,omitempty"`
	SkewedColumnValueLocationMaps map[string]string `json:"SkewedColumnValueLocationMaps,omitempty"`
}

// SchemaID identifies a Schema Registry schema.
type SchemaID struct {
	SchemaArn    string `json:"SchemaArn,omitempty"`
	SchemaName   string `json:"SchemaName,omitempty"`
	RegistryName string `json:"RegistryName,omitempty"`
}

// SchemaReference points a StorageDescriptor at a Schema Registry schema.
type SchemaReference struct {
	SchemaId            *SchemaID `json:"SchemaId,omitempty"`
	SchemaVersionId     string    `json:"SchemaVersionId,omitempty"`
	SchemaVersionNumber *int64    `json:"SchemaVersionNumber,omitempty"`
}

// StorageDescriptor is where a table's or partition's data lives and how it
// is laid out. The optional scalars are pointers so an unset member stays
// unset rather than coming back as a zero the caller never sent.
type StorageDescriptor struct {
	Columns                []Column          `json:"Columns,omitempty"`
	Location               string            `json:"Location,omitempty"`
	AdditionalLocations    []string          `json:"AdditionalLocations,omitempty"`
	InputFormat            string            `json:"InputFormat,omitempty"`
	OutputFormat           string            `json:"OutputFormat,omitempty"`
	Compressed             *bool             `json:"Compressed,omitempty"`
	NumberOfBuckets        *int32            `json:"NumberOfBuckets,omitempty"`
	SerdeInfo              *SerDeInfo        `json:"SerdeInfo,omitempty"`
	BucketColumns          []string          `json:"BucketColumns,omitempty"`
	SortColumns            []Order           `json:"SortColumns,omitempty"`
	Parameters             map[string]string `json:"Parameters,omitempty"`
	SkewedInfo             *SkewedInfo       `json:"SkewedInfo,omitempty"`
	StoredAsSubDirectories *bool             `json:"StoredAsSubDirectories,omitempty"`
	SchemaReference        *SchemaReference  `json:"SchemaReference,omitempty"`
}

// withoutColumns returns a copy of sd with no Columns, for GetPartitions'
// ExcludeColumnSchema. The stored record is left untouched.
func (sd *StorageDescriptor) withoutColumns() *StorageDescriptor {
	if sd == nil {
		return nil
	}
	c := *sd
	c.Columns = nil
	return &c
}

// TableIdentifier is a resource link's target table.
type TableIdentifier struct {
	CatalogId    string `json:"CatalogId,omitempty"`
	DatabaseName string `json:"DatabaseName,omitempty"`
	Name         string `json:"Name,omitempty"`
	Region       string `json:"Region,omitempty"`
}

// DatabaseIdentifier is a resource link's target database.
type DatabaseIdentifier struct {
	CatalogId    string `json:"CatalogId,omitempty"`
	DatabaseName string `json:"DatabaseName,omitempty"`
	Region       string `json:"Region,omitempty"`
}

// DatabaseInput is CreateDatabase's and UpdateDatabase's definition.
type DatabaseInput struct {
	Name                          string              `json:"Name"`
	Description                   string              `json:"Description,omitempty"`
	LocationUri                   string              `json:"LocationUri,omitempty"`
	Parameters                    map[string]string   `json:"Parameters,omitempty"`
	CreateTableDefaultPermissions []any               `json:"CreateTableDefaultPermissions,omitempty"`
	TargetDatabase                *DatabaseIdentifier `json:"TargetDatabase,omitempty"`
	FederatedDatabase             any                 `json:"FederatedDatabase,omitempty"`
}

// Database is the wire shape GetDatabase and GetDatabases return. The AWS
// model's Database carries no Tags member, so tags are never embedded here;
// see databaseRecord.
type Database struct {
	Name                          string              `json:"Name"`
	Description                   string              `json:"Description,omitempty"`
	LocationUri                   string              `json:"LocationUri,omitempty"`
	Parameters                    map[string]string   `json:"Parameters,omitempty"`
	CreateTime                    float64             `json:"CreateTime,omitempty"`
	CreateTableDefaultPermissions []any               `json:"CreateTableDefaultPermissions,omitempty"`
	TargetDatabase                *DatabaseIdentifier `json:"TargetDatabase,omitempty"`
	CatalogId                     string              `json:"CatalogId,omitempty"`
	FederatedDatabase             any                 `json:"FederatedDatabase,omitempty"`
}

// TableInput is CreateTable's and UpdateTable's definition.
type TableInput struct {
	Name              string             `json:"Name"`
	Description       string             `json:"Description,omitempty"`
	Owner             string             `json:"Owner,omitempty"`
	LastAccessTime    float64            `json:"LastAccessTime,omitempty"`
	LastAnalyzedTime  float64            `json:"LastAnalyzedTime,omitempty"`
	Retention         int32              `json:"Retention,omitempty"`
	StorageDescriptor *StorageDescriptor `json:"StorageDescriptor,omitempty"`
	PartitionKeys     []Column           `json:"PartitionKeys,omitempty"`
	ViewOriginalText  string             `json:"ViewOriginalText,omitempty"`
	ViewExpandedText  string             `json:"ViewExpandedText,omitempty"`
	TableType         string             `json:"TableType,omitempty"`
	Parameters        map[string]string  `json:"Parameters,omitempty"`
	TargetTable       *TableIdentifier   `json:"TargetTable,omitempty"`
	ViewDefinition    map[string]any     `json:"ViewDefinition,omitempty"`
}

// Table is the wire shape GetTable, GetTables and the table-version
// operations return. The AWS model's Table carries no Tags member, so tags
// are never embedded here; see tableRecord.
//
// PartitionKeys, Retention and IsRegisteredWithLakeFormation carry no
// omitempty: AWS returns all three on every table, as `[]`, `0` and `false`
// when nothing set them, and Hive-style readers index PartitionKeys directly.
type Table struct {
	Name                          string             `json:"Name"`
	DatabaseName                  string             `json:"DatabaseName"`
	Description                   string             `json:"Description,omitempty"`
	Owner                         string             `json:"Owner,omitempty"`
	CreateTime                    float64            `json:"CreateTime,omitempty"`
	UpdateTime                    float64            `json:"UpdateTime,omitempty"`
	LastAccessTime                float64            `json:"LastAccessTime,omitempty"`
	LastAnalyzedTime              float64            `json:"LastAnalyzedTime,omitempty"`
	Retention                     int32              `json:"Retention"`
	StorageDescriptor             *StorageDescriptor `json:"StorageDescriptor,omitempty"`
	PartitionKeys                 []Column           `json:"PartitionKeys"`
	ViewOriginalText              string             `json:"ViewOriginalText,omitempty"`
	ViewExpandedText              string             `json:"ViewExpandedText,omitempty"`
	TableType                     string             `json:"TableType,omitempty"`
	Parameters                    map[string]string  `json:"Parameters,omitempty"`
	IsRegisteredWithLakeFormation bool               `json:"IsRegisteredWithLakeFormation"`
	TargetTable                   *TableIdentifier   `json:"TargetTable,omitempty"`
	CatalogId                     string             `json:"CatalogId,omitempty"`
	VersionId                     string             `json:"VersionId,omitempty"`
	ViewDefinition                map[string]any     `json:"ViewDefinition,omitempty"`
}

// TableVersion is one entry of GetTableVersion(s).
type TableVersion struct {
	Table     *Table `json:"Table,omitempty"`
	VersionId string `json:"VersionId,omitempty"`
}

// PartitionInput is a partition's definition.
type PartitionInput struct {
	Values            []string           `json:"Values"`
	LastAccessTime    float64            `json:"LastAccessTime,omitempty"`
	LastAnalyzedTime  float64            `json:"LastAnalyzedTime,omitempty"`
	StorageDescriptor *StorageDescriptor `json:"StorageDescriptor,omitempty"`
	Parameters        map[string]string  `json:"Parameters,omitempty"`
}

// Partition is the wire shape of one table partition.
type Partition struct {
	Values            []string           `json:"Values"`
	DatabaseName      string             `json:"DatabaseName"`
	TableName         string             `json:"TableName"`
	CreationTime      float64            `json:"CreationTime,omitempty"`
	LastAccessTime    float64            `json:"LastAccessTime,omitempty"`
	LastAnalyzedTime  float64            `json:"LastAnalyzedTime,omitempty"`
	StorageDescriptor *StorageDescriptor `json:"StorageDescriptor,omitempty"`
	Parameters        map[string]string  `json:"Parameters,omitempty"`
	CatalogId         string             `json:"CatalogId,omitempty"`
}

// PartitionValueList names one partition by its values, as the batch
// partition operations do.
type PartitionValueList struct {
	Values []string `json:"Values" cbor:"Values"`
}

// ErrorDetail is a batch operation's per-item failure.
type ErrorDetail struct {
	ErrorCode    string `json:"ErrorCode,omitempty"`
	ErrorMessage string `json:"ErrorMessage,omitempty"`
}

// PartitionError is a batch partition operation's per-partition failure.
type PartitionError struct {
	PartitionValues []string     `json:"PartitionValues,omitempty"`
	ErrorDetail     *ErrorDetail `json:"ErrorDetail,omitempty"`
}

// TableError is BatchDeleteTable's per-table failure.
type TableError struct {
	TableName   string       `json:"TableName,omitempty"`
	ErrorDetail *ErrorDetail `json:"ErrorDetail,omitempty"`
}

// TableVersionError is BatchDeleteTableVersion's per-version failure.
type TableVersionError struct {
	TableName   string       `json:"TableName,omitempty"`
	VersionId   string       `json:"VersionId,omitempty"`
	ErrorDetail *ErrorDetail `json:"ErrorDetail,omitempty"`
}
