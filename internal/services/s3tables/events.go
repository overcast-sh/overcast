package s3tables

import (
	"context"
	"slices"
	"strconv"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/icebergmeta"
)

// InitBus wires the event bus that table creates, renames, deletes and
// commits are published to. Until it is called nothing is published.
func (s *Service) InitBus(b *events.Bus) { s.bus = b }

// metadataSwap is one move of a table's metadata pointer that swapMetadata
// made: the table as it was saved, the metadata file it moved from (empty
// for its first), whether the swap created the table, and the new metadata
// when the proposer had it parsed.
type metadataSwap struct {
	table    tableRecord
	previous string
	created  bool
	metadata *icebergmeta.Metadata
}

func tablePayload(t *tableRecord) events.S3TablesTablePayload {
	return events.S3TablesTablePayload{ARN: t.ARN, Bucket: t.Bucket, Namespace: t.Namespace, Name: t.Name}
}

// publishTable reports that table t was created, renamed or deleted.
func (s *Service) publishTable(ctx context.Context, typ events.Type, t *tableRecord) {
	s.publish(ctx, typ, tablePayload(t))
}

// publishSwap reports a metadata swap: the table's creation when the swap
// created it, then the commit. The commit names the new metadata's current
// snapshot; metadata the proposer did not parse — an
// UpdateTableMetadataLocation's, written by the client — is read back for it,
// and a file that cannot be read leaves the snapshot out.
func (s *Service) publishSwap(ctx context.Context, swap *metadataSwap) {
	if s.bus == nil {
		return
	}
	if swap.created {
		s.publishTable(ctx, events.S3TablesTableCreated, &swap.table)
	}
	meta := swap.metadata
	if meta == nil && s.getObject != nil {
		meta, _ = s.readMetadata(ctx, swap.table.MetadataLocation)
	}
	snapshotID, operation := currentSnapshot(meta)
	s.publish(ctx, events.S3TablesTableCommitted, events.S3TablesCommitPayload{
		S3TablesTablePayload:     tablePayload(&swap.table),
		PreviousMetadataLocation: swap.previous,
		MetadataLocation:         swap.table.MetadataLocation,
		SnapshotID:               snapshotID,
		Operation:                operation,
	})
}

func (s *Service) publish(ctx context.Context, typ events.Type, payload any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, events.Event{Type: typ, Time: s.clk.Now(), Source: serviceName, Payload: payload})
}

// currentSnapshot is the id, in decimal, and the summary's operation of m's
// current snapshot; both are empty when m is nil or has no current snapshot.
func currentSnapshot(m *icebergmeta.Metadata) (id, operation string) {
	if m == nil {
		return "", ""
	}
	i := slices.IndexFunc(m.Snapshots, func(sn icebergmeta.Snapshot) bool { return sn.SnapshotID == m.CurrentSnapshotID })
	if i < 0 {
		return "", ""
	}
	return strconv.FormatInt(m.CurrentSnapshotID, 10), m.Snapshots[i].Summary["operation"]
}
