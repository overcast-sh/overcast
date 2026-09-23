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

// metadataFileName names a new table's first metadata file.
func metadataFileName(fileUUID string) string {
	return icebergmeta.FileName(0, fileUUID)
}

// buildInitialMetadata turns metadata.iceberg into the table's first
// metadata.json. Iceberg assigns a new table's column ids itself, 1..n in
// declaration order; a column id the caller supplied is honoured only as the
// name partition and sort fields refer to that column by. A field without a
// declared id is addressed by its position (1..n) unless another field
// declared that number; a declared id used twice is refused.
func buildInitialMetadata(tableUUID, location string, now time.Time, in *icebergMetadata) ([]byte, *protocol.AWSError) {
	columns := make([]icebergmeta.ColumnInput, 0, len(in.Schema.Fields))
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
		columns = append(columns, icebergmeta.ColumnInput{Name: f.Name, Type: f.Type, Required: f.Required})
	}
	source := func(id int) int {
		if mapped, ok := idMap[id]; ok {
			return mapped
		}
		return -1 // refused by icebergmeta as an unknown source id
	}

	table := icebergmeta.TableInput{
		TableUUID:     tableUUID,
		Location:      location,
		LastUpdatedMS: now.UnixMilli(),
		Columns:       columns,
		Properties:    in.Properties,
	}
	if in.PartitionSpec != nil {
		if in.PartitionSpec.SpecID != nil && *in.PartitionSpec.SpecID != icebergmeta.InitialSpecID {
			return nil, badRequest("A new table's partition spec must have spec-id 0.")
		}
		for _, p := range in.PartitionSpec.Fields {
			table.Partition = append(table.Partition, icebergmeta.PartitionFieldInput{
				SourceID: source(p.SourceID), FieldID: p.FieldID, Name: p.Name, Transform: p.Transform,
			})
		}
	}
	if in.WriteOrder != nil && len(in.WriteOrder.Fields) > 0 {
		table.SortOrderID = in.WriteOrder.OrderID
		for _, f := range in.WriteOrder.Fields {
			table.SortFields = append(table.SortFields, icebergmeta.SortFieldInput{
				SourceID: source(f.SourceID), Transform: f.Transform, Direction: f.Direction, NullOrder: f.NullOrder,
			})
		}
	}

	meta, err := icebergmeta.New(table)
	if err != nil {
		var inv *icebergmeta.ErrInvalid
		if errors.As(err, &inv) {
			return nil, badRequest(inv.Reason)
		}
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return raw, nil
}
