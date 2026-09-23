// Package icebergmeta builds Apache Iceberg table metadata — the
// metadata.json document an Iceberg catalog points a table at.
//
// Overcast writes that document itself where AWS does: S3 Tables' CreateTable
// with a schema (and, later, the Iceberg REST catalog's commits and Glue's
// OpenTableFormatInput). It hand-rolls the format-version 2 model rather than
// depending on apache/iceberg-go, which would pull arrow-go into every binary
// for a document of a dozen fields. Only metadata is written here; data and
// manifest files are always the engine's or the client's.
//
// Spec: https://iceberg.apache.org/spec/#table-metadata-fields
package icebergmeta

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// FormatVersion is the Iceberg table format this package writes.
const FormatVersion = 2

// Identifiers the spec gives an initial table.
const (
	// InitialSchemaID is the id of a new table's only schema.
	InitialSchemaID = 0
	// InitialSpecID is the id of a new table's partition spec.
	InitialSpecID = 0
	// UnsortedOrderID is the id the spec reserves for the unsorted order.
	UnsortedOrderID = 0
	// firstPartitionFieldID is where partition field ids start; the reference
	// implementation reports 999 as the last id of a spec with no fields.
	firstPartitionFieldID = 1000
)

// Field is one top-level column of a schema.
type Field struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     string `json:"type"`
	Doc      string `json:"doc,omitempty"`
}

// Schema is an Iceberg struct schema.
type Schema struct {
	Type               string  `json:"type"`
	SchemaID           int     `json:"schema-id"`
	IdentifierFieldIDs []int   `json:"identifier-field-ids,omitempty"`
	Fields             []Field `json:"fields"`
}

// PartitionField is one field of a partition spec.
type PartitionField struct {
	SourceID  int    `json:"source-id"`
	FieldID   int    `json:"field-id"`
	Name      string `json:"name"`
	Transform string `json:"transform"`
}

// PartitionSpec is an Iceberg partition spec.
type PartitionSpec struct {
	SpecID int              `json:"spec-id"`
	Fields []PartitionField `json:"fields"`
}

// SortField is one field of a sort order.
type SortField struct {
	SourceID  int    `json:"source-id"`
	Transform string `json:"transform"`
	Direction string `json:"direction"`
	NullOrder string `json:"null-order"`
}

// SortOrder is an Iceberg sort order.
type SortOrder struct {
	OrderID int         `json:"order-id"`
	Fields  []SortField `json:"fields"`
}

// Metadata is a format-version 2 table metadata document. Field order follows
// the spec's table so the written JSON reads the way the spec does.
type Metadata struct {
	FormatVersion      int               `json:"format-version"`
	TableUUID          string            `json:"table-uuid"`
	Location           string            `json:"location"`
	LastSequenceNumber int64             `json:"last-sequence-number"`
	LastUpdatedMS      int64             `json:"last-updated-ms"`
	LastColumnID       int               `json:"last-column-id"`
	CurrentSchemaID    int               `json:"current-schema-id"`
	Schemas            []Schema          `json:"schemas"`
	DefaultSpecID      int               `json:"default-spec-id"`
	PartitionSpecs     []PartitionSpec   `json:"partition-specs"`
	LastPartitionID    int               `json:"last-partition-id"`
	DefaultSortOrderID int               `json:"default-sort-order-id"`
	SortOrders         []SortOrder       `json:"sort-orders"`
	Properties         map[string]string `json:"properties"`
	Snapshots          []json.RawMessage `json:"snapshots"`
	SnapshotLog        []json.RawMessage `json:"snapshot-log"`
	MetadataLog        []json.RawMessage `json:"metadata-log"`
	Refs               map[string]any    `json:"refs"`
}

// ColumnInput is a column as a caller declares it: a name, an Iceberg type and
// whether it is required. Ids are assigned by New, as Iceberg assigns fresh
// ids to a new table's schema whatever the caller proposed.
type ColumnInput struct {
	Name     string
	Type     string
	Required bool
	Doc      string
}

// PartitionFieldInput is a partition field as a caller declares it. FieldID
// is optional (zero means "assign one").
type PartitionFieldInput struct {
	SourceID  int
	FieldID   int
	Name      string
	Transform string
}

// SortFieldInput is a sort field as a caller declares it.
type SortFieldInput struct {
	SourceID  int
	Transform string
	Direction string
	NullOrder string
}

// TableInput is everything New needs to describe a new table.
type TableInput struct {
	TableUUID     string
	Location      string
	LastUpdatedMS int64
	Columns       []ColumnInput
	Partition     []PartitionFieldInput
	SortOrderID   int
	SortFields    []SortFieldInput
	Properties    map[string]string
}

// ErrInvalid reports a table definition the spec does not allow. Its message
// is written for the API caller.
type ErrInvalid struct{ Reason string }

func (e *ErrInvalid) Error() string { return e.Reason }

func invalid(format string, args ...any) error {
	return &ErrInvalid{Reason: fmt.Sprintf(format, args...)}
}

// primitiveTypes are the format-version 2 primitive type names without
// parameters. The nanosecond timestamps and the other version 3 additions are
// deliberately absent: a v2 table that carries one cannot be read.
var primitiveTypes = map[string]bool{
	"boolean": true, "int": true, "long": true, "float": true, "double": true,
	"date": true, "time": true, "timestamp": true, "timestamptz": true,
	"string": true, "uuid": true, "binary": true,
}

var (
	decimalType = regexp.MustCompile(`^decimal\(\s*(\d+)\s*,\s*(\d+)\s*\)$`)
	fixedType   = regexp.MustCompile(`^fixed\[\s*(\d+)\s*\]$`)
)

// canonicalPrimitive returns typ in the spelling the spec and the reference
// readers parse — "decimal(P, S)", "fixed[N]" — and false when it is not a
// version 2 primitive. Nested types (struct, list, map) are JSON objects in
// the spec, which a string-typed column cannot carry.
func canonicalPrimitive(typ string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(typ))
	if primitiveTypes[t] {
		return t, true
	}
	if m := decimalType.FindStringSubmatch(t); m != nil {
		return "decimal(" + m[1] + ", " + m[2] + ")", true
	}
	if m := fixedType.FindStringSubmatch(t); m != nil {
		return "fixed[" + m[1] + "]", true
	}
	return "", false
}

// New builds the metadata of a table that has no snapshots yet: one schema
// (id 0) with field ids assigned 1..n in declaration order, the given
// partition spec as spec 0 (unpartitioned when empty) and the given sort order
// (unsorted, id 0, when empty).
func New(in TableInput) (*Metadata, error) {
	if in.TableUUID == "" || in.Location == "" {
		return nil, invalid("a table needs a UUID and a location")
	}
	if len(in.Columns) == 0 {
		return nil, invalid("The schema must contain at least one field.")
	}
	fields := make([]Field, 0, len(in.Columns))
	seen := make(map[string]bool, len(in.Columns))
	for i, c := range in.Columns {
		if c.Name == "" {
			return nil, invalid("Schema field %d has no name.", i)
		}
		if seen[c.Name] {
			return nil, invalid("Schema field name %q is used more than once.", c.Name)
		}
		seen[c.Name] = true
		typ, ok := canonicalPrimitive(c.Type)
		if !ok {
			return nil, invalid("Schema field %q has an unsupported type %q.", c.Name, c.Type)
		}
		fields = append(fields, Field{
			ID:       i + 1,
			Name:     c.Name,
			Required: c.Required,
			Type:     typ,
			Doc:      c.Doc,
		})
	}

	spec := PartitionSpec{SpecID: InitialSpecID, Fields: []PartitionField{}}
	lastPartitionID := firstPartitionFieldID - 1
	for _, p := range in.Partition {
		if p.FieldID != 0 && p.FieldID > lastPartitionID {
			lastPartitionID = p.FieldID
		}
	}
	partitionIDs := map[int]bool{}
	partitionNames := map[string]bool{}
	for _, p := range in.Partition {
		if p.SourceID < 1 || p.SourceID > len(fields) {
			return nil, invalid("Partition field %q refers to unknown source id %d.", p.Name, p.SourceID)
		}
		if p.Name == "" || p.Transform == "" {
			return nil, invalid("A partition field needs a name and a transform.")
		}
		id := p.FieldID
		if id == 0 {
			// Explicit ids were taken into account above, so an assigned one
			// can never collide with them.
			lastPartitionID++
			id = lastPartitionID
		}
		if id < firstPartitionFieldID || partitionIDs[id] {
			return nil, invalid("Partition field id %d is invalid or used more than once.", id)
		}
		if partitionNames[p.Name] {
			return nil, invalid("Partition field name %q is used more than once.", p.Name)
		}
		partitionIDs[id], partitionNames[p.Name] = true, true
		spec.Fields = append(spec.Fields, PartitionField{SourceID: p.SourceID, FieldID: id, Name: p.Name, Transform: p.Transform})
	}

	orders := []SortOrder{{OrderID: UnsortedOrderID, Fields: []SortField{}}}
	defaultOrder := UnsortedOrderID
	if len(in.SortFields) > 0 {
		if in.SortOrderID == UnsortedOrderID {
			return nil, invalid("Sort order id 0 is reserved for the unsorted order.")
		}
		sorted := SortOrder{OrderID: in.SortOrderID, Fields: make([]SortField, 0, len(in.SortFields))}
		for _, f := range in.SortFields {
			if f.SourceID < 1 || f.SourceID > len(fields) {
				return nil, invalid("Sort field refers to unknown source id %d.", f.SourceID)
			}
			if f.Direction != "asc" && f.Direction != "desc" {
				return nil, invalid("Sort direction %q must be asc or desc.", f.Direction)
			}
			if f.NullOrder != "nulls-first" && f.NullOrder != "nulls-last" {
				return nil, invalid("Sort null-order %q must be nulls-first or nulls-last.", f.NullOrder)
			}
			if f.Transform == "" {
				return nil, invalid("A sort field needs a transform.")
			}
			sorted.Fields = append(sorted.Fields, SortField(f))
		}
		orders = append(orders, sorted)
		defaultOrder = in.SortOrderID
	}

	props := in.Properties
	if props == nil {
		props = map[string]string{}
	}
	return &Metadata{
		FormatVersion:      FormatVersion,
		TableUUID:          in.TableUUID,
		Location:           in.Location,
		LastUpdatedMS:      in.LastUpdatedMS,
		LastColumnID:       len(fields),
		CurrentSchemaID:    InitialSchemaID,
		Schemas:            []Schema{{Type: "struct", SchemaID: InitialSchemaID, Fields: fields}},
		DefaultSpecID:      InitialSpecID,
		PartitionSpecs:     []PartitionSpec{spec},
		LastPartitionID:    lastPartitionID,
		DefaultSortOrderID: defaultOrder,
		SortOrders:         orders,
		Properties:         props,
		Snapshots:          []json.RawMessage{},
		SnapshotLog:        []json.RawMessage{},
		MetadataLog:        []json.RawMessage{},
		Refs:               map[string]any{},
	}, nil
}

// FileName is the name of the n-th metadata file of a table, in the
// "<version>-<uuid>.metadata.json" form the reference implementation writes.
func FileName(version int, fileUUID string) string {
	return fmt.Sprintf("%05d-%s.metadata.json", version, fileUUID)
}
