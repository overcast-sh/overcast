package glue

import (
	"context"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// InitBus wires the event bus that table and partition changes are published
// to. Until it is called nothing is published.
func (s *Service) InitBus(b *events.Bus) { s.bus = b }

// tableARN is arn:aws:glue:<region>:<account>:table/<database>/<table>, the
// ARN AWS gives a Data Catalog table.
func (s *Service) tableARN(ctx context.Context, dbName, tableName string) string {
	region := middleware.RegionFromContext(ctx, s.cfg.Region)
	return protocol.ARN(region, s.cfg.AccountID, serviceName, "table/"+dbName+"/"+tableName)
}

// publishTableChanged reports that a table was created, updated or deleted;
// change is one of the events.GlueTable* constants.
func (s *Service) publishTableChanged(ctx context.Context, dbName, tableName, change string) {
	s.publish(ctx, events.GlueTableChanged, dbName, tableName, change)
}

// publishPartitionsChanged reports that a request changed at least one of a
// table's partitions.
func (s *Service) publishPartitionsChanged(ctx context.Context, dbName, tableName string) {
	s.publish(ctx, events.GluePartitionsChanged, dbName, tableName, "")
}

// publishIfAnyApplied reports a batch partition operation over requested
// partitions of t, unless every one of them failed.
func (s *Service) publishIfAnyApplied(ctx context.Context, t *tableRecord, requested int, resp *batchPartitionErrorsResp) {
	if len(resp.Errors) < requested {
		s.publishPartitionsChanged(ctx, t.DatabaseName, t.Name)
	}
}

func (s *Service) publish(ctx context.Context, t events.Type, dbName, tableName, change string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, events.Event{
		Type:   t,
		Time:   s.clk.Now(),
		Source: serviceName,
		Payload: events.GlueTablePayload{
			Database: dbName, Table: tableName, ARN: s.tableARN(ctx, dbName, tableName), Change: change,
		},
	})
}
