package athena

import (
	"context"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
)

// InitBus wires the event bus that query state changes are published to.
// Until it is called nothing is published.
func (s *Service) InitBus(b *events.Bus) { s.bus = b }

// tableTarget is a DDL statement about one table, which it names.
type tableTarget interface{ target() tableRef }

// publishQueryState reports the state qe has just been stored in.
func (s *Service) publishQueryState(ctx context.Context, qe *QueryExecution) {
	if s.bus == nil {
		return
	}
	database := orDefault(qe.QueryExecutionContext.Database, defaultDatabase)
	s.bus.Publish(ctx, events.Event{
		Type:   events.AthenaQueryStateChanged,
		Time:   s.clk.Now(),
		Source: serviceName,
		Payload: events.AthenaQueryStatePayload{
			QueryExecutionID: qe.QueryExecutionId,
			WorkGroup:        qe.WorkGroup,
			State:            qe.Status.State,
			Catalog:          qe.QueryExecutionContext.Catalog,
			Database:         database,
			Tables:           statementTables(qe.Query, database),
		},
	})
}

// statementTables is the table a DDL statement names, as "database.table"
// with an unqualified name resolved in database. Any other statement — the
// engine's SQL, which Overcast does not parse — names none it knows of.
func statementTables(query, database string) []string {
	stmt, _ := parseDDL(query)
	target, ok := stmt.(tableTarget)
	if !ok {
		return nil
	}
	ref := target.target()
	return []string{orDefault(ref.Database, database) + "." + strings.ToLower(ref.Table)}
}
