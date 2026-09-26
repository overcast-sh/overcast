package glue

import (
	"testing"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/state"
	"go.uber.org/zap"
)

var allGlueOps = []string{
	"GetCatalog", "GetCatalogs",
	"CreateDatabase", "GetDatabase", "GetDatabases", "UpdateDatabase", "DeleteDatabase",
	"CreateTable", "GetTable", "GetTables", "UpdateTable", "DeleteTable", "BatchDeleteTable",
	"GetTableVersion", "GetTableVersions", "DeleteTableVersion", "BatchDeleteTableVersion",
	"CreatePartition", "BatchCreatePartition", "GetPartition", "GetPartitions",
	"BatchGetPartition", "UpdatePartition", "DeletePartition", "BatchDeletePartition",
	"UpdateColumnStatisticsForTable", "UpdateColumnStatisticsForPartition",
	"GetColumnStatisticsForTable", "GetColumnStatisticsForPartition",
	"DeleteColumnStatisticsForTable", "DeleteColumnStatisticsForPartition",
	"TagResource", "UntagResource", "GetTags",
}

func TestTypedOps_matchAllOperations(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1", AccountID: "123456789012"}
	s := New(cfg, state.NewMemoryStore(), zap.NewNop(), clock.New())

	if len(s.typedOp) != len(allGlueOps) {
		t.Fatalf("typed op count = %d, want %d", len(s.typedOp), len(allGlueOps))
	}
	for _, name := range allGlueOps {
		operation, ok := s.typedOp[name]
		if !ok {
			t.Fatalf("missing typed operation %s", name)
		}
		if operation.Name() != name {
			t.Fatalf("typed operation %s reports name %s", name, operation.Name())
		}
		if _, raw := operation.(*op.Raw); raw {
			t.Fatalf("typed operation %s still uses raw adapter", name)
		}
	}
}
