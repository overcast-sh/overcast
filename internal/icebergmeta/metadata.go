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
// The API is Decision 4's in docs/plans/athena-s3tables-iceberg.md: New takes
// the table in Iceberg's own types and hides how the document is built.
// Parse and Commit join it with the Iceberg REST catalog (#2069).
//
// Spec: https://iceberg.apache.org/spec/#table-metadata-fields
package icebergmeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
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

// CreateSpec describes a new table in Iceberg's own terms. Field ids are
// ignored: New assigns 1..n in declaration order, as Iceberg assigns fresh ids
// to a new table's schema whatever the caller proposed. A partition field's
// FieldID is optional (zero means "assign one"); partition and sort fields
// name their column by its assigned id.
type CreateSpec struct {
	TableUUID       string
	Location        string
	Fields          []Field
	PartitionFields []PartitionField
	SortOrderID     int
	SortFields      []SortField
	Properties      map[string]string
}

// ErrInvalid is matched (errors.Is) by every error New returns for a table
// definition the spec does not allow. The error's message is written for the
// API caller.
var ErrInvalid = errors.New("icebergmeta: invalid table definition")

type invalidError struct{ reason string }

func (e invalidError) Error() string        { return e.reason }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error {
	return invalidError{reason: fmt.Sprintf(format, args...)}
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

// New builds the metadata of a table that has no snapshots yet, last updated
// at now: one schema (id 0) with field ids assigned 1..n in declaration order,
// the given partition spec as spec 0 (unpartitioned when empty) and the given
// sort order (unsorted, id 0, when empty).
func New(spec CreateSpec, now time.Time) (*Metadata, error) {
	if spec.TableUUID == "" || spec.Location == "" {
		return nil, invalid("a table needs a UUID and a location")
	}
	schema, err := newSchema(spec.Fields)
	if err != nil {
		return nil, err
	}
	partitionSpec, lastPartitionID, err := newPartitionSpec(spec.PartitionFields, len(schema.Fields))
	if err != nil {
		return nil, err
	}
	orders, defaultOrder, err := newSortOrders(spec.SortOrderID, spec.SortFields, len(schema.Fields))
	if err != nil {
		return nil, err
	}
	props := spec.Properties
	if props == nil {
		props = map[string]string{}
	}
	return &Metadata{
		FormatVersion:      FormatVersion,
		TableUUID:          spec.TableUUID,
		Location:           spec.Location,
		LastUpdatedMS:      now.UnixMilli(),
		LastColumnID:       len(schema.Fields),
		CurrentSchemaID:    InitialSchemaID,
		Schemas:            []Schema{schema},
		DefaultSpecID:      InitialSpecID,
		PartitionSpecs:     []PartitionSpec{partitionSpec},
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

// newSchema is the initial schema: the columns in order, ids 1..n, types in
// their canonical spelling.
func newSchema(columns []Field) (Schema, error) {
	if len(columns) == 0 {
		return Schema{}, invalid("The schema must contain at least one field.")
	}
	fields := make([]Field, 0, len(columns))
	seen := make(map[string]bool, len(columns))
	for i, c := range columns {
		if c.Name == "" {
			return Schema{}, invalid("Schema field %d has no name.", i)
		}
		if seen[c.Name] {
			return Schema{}, invalid("Schema field name %q is used more than once.", c.Name)
		}
		seen[c.Name] = true
		typ, ok := canonicalPrimitive(c.Type)
		if !ok {
			return Schema{}, invalid("Schema field %q has an unsupported type %q.", c.Name, c.Type)
		}
		fields = append(fields, Field{ID: i + 1, Name: c.Name, Required: c.Required, Type: typ, Doc: c.Doc})
	}
	return Schema{Type: "struct", SchemaID: InitialSchemaID, Fields: fields}, nil
}

// newPartitionSpec is spec 0 over a schema of columnCount columns, with the
// spec's last partition field id. Explicit field ids are honoured; the rest
// are assigned above the highest explicit one, so the two never collide.
func newPartitionSpec(in []PartitionField, columnCount int) (PartitionSpec, int, error) {
	spec := PartitionSpec{SpecID: InitialSpecID, Fields: []PartitionField{}}
	lastID := firstPartitionFieldID - 1
	for _, p := range in {
		lastID = max(lastID, p.FieldID)
	}
	ids := map[int]bool{}
	names := map[string]bool{}
	for _, p := range in {
		if p.SourceID < 1 || p.SourceID > columnCount {
			return PartitionSpec{}, 0, invalid("Partition field %q refers to unknown source id %d.", p.Name, p.SourceID)
		}
		if p.Name == "" || p.Transform == "" {
			return PartitionSpec{}, 0, invalid("A partition field needs a name and a transform.")
		}
		if p.FieldID == 0 {
			lastID++
			p.FieldID = lastID
		}
		if p.FieldID < firstPartitionFieldID || ids[p.FieldID] {
			return PartitionSpec{}, 0, invalid("Partition field id %d is invalid or used more than once.", p.FieldID)
		}
		if names[p.Name] {
			return PartitionSpec{}, 0, invalid("Partition field name %q is used more than once.", p.Name)
		}
		ids[p.FieldID], names[p.Name] = true, true
		spec.Fields = append(spec.Fields, p)
	}
	return spec, lastID, nil
}

// newSortOrders is the unsorted order plus, when fields are given, the order
// orderID over them, with the id of the default order.
func newSortOrders(orderID int, in []SortField, columnCount int) ([]SortOrder, int, error) {
	orders := []SortOrder{{OrderID: UnsortedOrderID, Fields: []SortField{}}}
	if len(in) == 0 {
		return orders, UnsortedOrderID, nil
	}
	if orderID == UnsortedOrderID {
		return nil, 0, invalid("Sort order id 0 is reserved for the unsorted order.")
	}
	for _, f := range in {
		if f.SourceID < 1 || f.SourceID > columnCount {
			return nil, 0, invalid("Sort field refers to unknown source id %d.", f.SourceID)
		}
		if f.Direction != "asc" && f.Direction != "desc" {
			return nil, 0, invalid("Sort direction %q must be asc or desc.", f.Direction)
		}
		if f.NullOrder != "nulls-first" && f.NullOrder != "nulls-last" {
			return nil, 0, invalid("Sort null-order %q must be nulls-first or nulls-last.", f.NullOrder)
		}
		if f.Transform == "" {
			return nil, 0, invalid("A sort field needs a transform.")
		}
	}
	return append(orders, SortOrder{OrderID: orderID, Fields: slices.Clone(in)}), orderID, nil
}

// MetadataPath is where the n-th metadata file of a table lives, relative to
// the table's location, in the "metadata/<version>-<uuid>.metadata.json" form
// the reference implementation writes.
func MetadataPath(version int, fileUUID string) string {
	return fmt.Sprintf("metadata/%05d-%s.metadata.json", version, fileUUID)
}
