package s3tables

// CreateTable's metadata.iceberg: translating the S3 Tables shape into the
// initial Iceberg metadata document internal/icebergmeta builds.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/icebergmeta"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// s3PutJSON is how the metadata document is stored in the warehouse.
var s3PutJSON = events.S3PutObjectOptions{ContentType: "application/json"}

// buildInitialMetadata turns metadata.iceberg into the table's first
// metadata.json. Iceberg assigns a new table's column ids itself, 1..n in
// declaration order; a column id the caller supplied is honoured only as the
// name partition and sort fields refer to that column by. A field without a
// declared id is addressed by its position (1..n) unless another field
// declared that number; a declared id used twice is refused.
func buildInitialMetadata(tableUUID, location string, now time.Time, in *icebergMetadata) ([]byte, *protocol.AWSError) {
	columns := make([]icebergmeta.Field, 0, len(in.Schema.Fields))
	idMap := make(map[int]int, len(in.Schema.Fields))
	for i, f := range in.Schema.Fields {
		if f.ID == nil {
			continue
		}
		if _, dup := idMap[*f.ID]; dup {
			return nil, badRequest(fmt.Sprintf("Schema field id %d is used more than once.", *f.ID))
		}
		idMap[*f.ID] = i + 1
	}
	for i, f := range in.Schema.Fields {
		if f.ID == nil {
			if _, taken := idMap[i+1]; !taken {
				idMap[i+1] = i + 1
			}
		}
		columns = append(columns, icebergmeta.Field{Name: f.Name, Type: f.Type, Required: f.Required})
	}
	source := func(id int) int {
		if mapped, ok := idMap[id]; ok {
			return mapped
		}
		return -1 // refused by icebergmeta as an unknown source id
	}

	spec := icebergmeta.CreateSpec{
		TableUUID:  tableUUID,
		Location:   location,
		Fields:     columns,
		Properties: in.Properties,
	}
	if in.PartitionSpec != nil {
		if in.PartitionSpec.SpecID != nil && *in.PartitionSpec.SpecID != icebergmeta.InitialSpecID {
			return nil, badRequest("A new table's partition spec must have spec-id 0.")
		}
		for _, p := range in.PartitionSpec.Fields {
			spec.PartitionFields = append(spec.PartitionFields, icebergmeta.PartitionField{
				SourceID: source(p.SourceID), FieldID: p.FieldID, Name: p.Name, Transform: p.Transform,
			})
		}
	}
	if in.WriteOrder != nil && len(in.WriteOrder.Fields) > 0 {
		spec.SortOrderID = in.WriteOrder.OrderID
		for _, f := range in.WriteOrder.Fields {
			spec.SortFields = append(spec.SortFields, icebergmeta.SortField{
				SourceID: source(f.SourceID), Transform: f.Transform, Direction: f.Direction, NullOrder: f.NullOrder,
			})
		}
	}

	meta, err := icebergmeta.New(spec, now)
	if err != nil {
		if errors.Is(err, icebergmeta.ErrInvalid) {
			return nil, badRequest(err.Error())
		}
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return raw, nil
}
