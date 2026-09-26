package s3tables

// CreateTable's metadata.iceberg: the S3 Tables shape, its translation into
// the initial Iceberg metadata document internal/icebergmeta builds, and the
// service's answers to icebergmeta's errors.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// ─── Shapes ───────────────────────────────────────────────────────────────────

// icebergSchemaField is SchemaField.
type icebergSchemaField struct {
	ID       *int   `json:"id,omitempty"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
}

type icebergSchema struct {
	Fields []icebergSchemaField `json:"fields"`
}

// icebergSchemaV2 is IcebergSchemaV2. Its fields are SchemaV2Field, whose
// members — id, name, required, type as an Iceberg type document, doc — are
// exactly the Iceberg spec's JSON for a field, so icebergmeta reads them.
type icebergSchemaV2 struct {
	Type               string              `json:"type"`
	SchemaID           *int                `json:"schema-id,omitempty"`
	IdentifierFieldIDs []int               `json:"identifier-field-ids,omitempty"`
	Fields             []icebergmeta.Field `json:"fields"`
}

type icebergPartitionField struct {
	SourceID  int    `json:"source-id"`
	Transform string `json:"transform"`
	Name      string `json:"name"`
	FieldID   int    `json:"field-id,omitempty"`
}

type icebergPartitionSpec struct {
	Fields []icebergPartitionField `json:"fields"`
	SpecID *int                    `json:"spec-id,omitempty"`
}

type icebergSortField struct {
	SourceID  int    `json:"source-id"`
	Transform string `json:"transform"`
	Direction string `json:"direction"`
	NullOrder string `json:"null-order"`
}

type icebergSortOrder struct {
	OrderID int                `json:"order-id"`
	Fields  []icebergSortField `json:"fields"`
}

type icebergMetadata struct {
	Schema        *icebergSchema        `json:"schema,omitempty"`
	SchemaV2      *icebergSchemaV2      `json:"schemaV2,omitempty"`
	PartitionSpec *icebergPartitionSpec `json:"partitionSpec,omitempty"`
	WriteOrder    *icebergSortOrder     `json:"writeOrder,omitempty"`
	Properties    map[string]string     `json:"properties,omitempty"`
}

type tableMetadata struct {
	Iceberg *icebergMetadata `json:"iceberg,omitempty"`
}

// schemaV2Struct is the only value SchemaV2FieldType allows.
const schemaV2Struct = "struct"

// validateIcebergSchemaChoice checks that metadata.iceberg declares its
// columns exactly one way. The API reference gives schema to primitive-only
// tables and schemaV2 to nested ones, and CloudFormation's AWS::S3Tables::Table
// documents the two as mutually exclusive; what the API itself answers to
// both has not been observed, so refusing them follows CloudFormation.
func validateIcebergSchemaChoice(in *icebergMetadata) *protocol.AWSError {
	switch {
	case in.Schema != nil && in.SchemaV2 != nil:
		return badRequest("Specify either metadata.iceberg.schema or metadata.iceberg.schemaV2, not both.")
	case in.SchemaV2 != nil && in.SchemaV2.Type != schemaV2Struct:
		return enumError("metadata.iceberg.schemaV2.type", in.SchemaV2.Type, schemaV2Struct)
	case in.Schema == nil && in.SchemaV2 == nil:
		return badRequest("metadata.iceberg.schema or metadata.iceberg.schemaV2 is required.")
	}
	return nil
}

// ─── Initial metadata ─────────────────────────────────────────────────────────

// s3PutJSON is how the metadata document is stored in the warehouse.
var s3PutJSON = events.S3PutObjectOptions{ContentType: "application/json"}

// createColumns is a request's columns in icebergmeta's terms: the fields, the
// identifier field ids, and source, which turns a partition or sort field's
// source-id into the id of the field it names.
type createColumns struct {
	fields        []icebergmeta.Field
	identifierIDs []int
	source        func(int) int
}

// buildInitialMetadata turns metadata.iceberg into the table's first
// metadata.json. Iceberg assigns a new table's field ids itself, from 1,
// depth-first, whatever the caller declared; the caller's ids only say which
// field a partition field, a sort field or an identifier field id means. The
// schema is id 0 whatever schemaV2's schema-id says, and a write order is
// order 1 whatever its order-id says, as Iceberg numbers every new table.
func buildInitialMetadata(tableUUID, location string, now time.Time, in *icebergMetadata) ([]byte, *protocol.AWSError) {
	columns, aerr := columnsOf(in)
	if aerr != nil {
		return nil, aerr
	}
	spec := icebergmeta.CreateSpec{
		TableUUID:          tableUUID,
		Location:           location,
		Fields:             columns.fields,
		IdentifierFieldIDs: columns.identifierIDs,
		Properties:         in.Properties,
	}
	if in.PartitionSpec != nil {
		if in.PartitionSpec.SpecID != nil && *in.PartitionSpec.SpecID != icebergmeta.InitialSpecID {
			return nil, badRequest("A new table's partition spec must have spec-id 0.")
		}
		for _, p := range in.PartitionSpec.Fields {
			spec.PartitionFields = append(spec.PartitionFields, icebergmeta.PartitionField{
				SourceID: columns.source(p.SourceID), FieldID: p.FieldID, Name: p.Name, Transform: p.Transform,
			})
		}
	}
	if in.WriteOrder != nil {
		for _, f := range in.WriteOrder.Fields {
			spec.SortFields = append(spec.SortFields, icebergmeta.SortField{
				SourceID: columns.source(f.SourceID), Transform: f.Transform, Direction: f.Direction, NullOrder: f.NullOrder,
			})
		}
	}

	meta, aerr := newMetadata(spec, now)
	if aerr != nil {
		return nil, aerr
	}
	return marshalMetadata(meta)
}

// columnsOf is the columns of whichever schema the request declares.
func columnsOf(in *icebergMetadata) (createColumns, *protocol.AWSError) {
	if v2 := in.SchemaV2; v2 != nil {
		// Every schemaV2 field declares its id, and icebergmeta follows each
		// reference from it to the field's fresh id itself.
		return createColumns{fields: v2.Fields, identifierIDs: v2.IdentifierFieldIDs, source: func(id int) int { return id }}, nil
	}
	return schemaColumns(in.Schema.Fields)
}

// nestedTypeName matches a type spelled the way Hive and SQL write a nested
// one: "list<string>", "struct<x:int>".
var nestedTypeName = regexp.MustCompile(`(?i)^\s*(struct|list|array|map)\s*<`)

// schemaColumns is the columns of a primitive-only schema, numbered 1..n in
// declaration order. A column id the caller supplied is honoured only as the
// name partition and sort fields refer to that column by. A field without a
// declared id is addressed by its position (1..n) unless another field
// declared that number; a declared id used twice is refused.
func schemaColumns(fields []icebergSchemaField) (createColumns, *protocol.AWSError) {
	idMap := make(map[int]int, len(fields))
	for i, f := range fields {
		if nestedTypeName.MatchString(f.Type) {
			return createColumns{}, badRequest(fmt.Sprintf("Schema field %q has type %q: metadata.iceberg.schema takes primitive types only; declare nested types with metadata.iceberg.schemaV2.", f.Name, f.Type))
		}
		if f.ID == nil {
			continue
		}
		if _, dup := idMap[*f.ID]; dup {
			return createColumns{}, badRequest(fmt.Sprintf("Schema field id %d is used more than once.", *f.ID))
		}
		idMap[*f.ID] = i + 1
	}
	columns := make([]icebergmeta.Field, 0, len(fields))
	for i, f := range fields {
		if f.ID == nil {
			if _, taken := idMap[i+1]; !taken {
				idMap[i+1] = i + 1
			}
		}
		columns = append(columns, icebergmeta.Field{ID: i + 1, Name: f.Name, Type: icebergmeta.PrimitiveType(f.Type), Required: f.Required})
	}
	source := func(id int) int {
		if mapped, ok := idMap[id]; ok {
			return mapped
		}
		return -1 // refused by icebergmeta as an unknown source id
	}
	return createColumns{fields: columns, source: source}, nil
}

// newMetadata is icebergmeta.New answered in the service's errors.
func newMetadata(spec icebergmeta.CreateSpec, now time.Time) (*icebergmeta.Metadata, *protocol.AWSError) {
	meta, err := icebergmeta.New(spec, now)
	if err != nil {
		return nil, icebergmetaError(err)
	}
	return meta, nil
}

func marshalMetadata(meta *icebergmeta.Metadata) ([]byte, *protocol.AWSError) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return raw, nil
}

// icebergmetaError answers an icebergmeta error: a table definition or update
// the spec forbids is the caller's BadRequestException, and a commit whose
// requirement the table no longer meets is the Iceberg REST catalog's
// CommitFailedException — another writer committed first.
func icebergmetaError(err error) *protocol.AWSError {
	switch {
	case errors.Is(err, icebergmeta.ErrInvalid):
		return badRequest(err.Error())
	case errors.Is(err, icebergmeta.ErrRequirementFailed):
		return icebergError(http.StatusConflict, "CommitFailedException", err.Error())
	default:
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
}
