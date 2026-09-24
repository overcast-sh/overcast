package athena

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

func newTestService(t *testing.T) (*Service, state.Store) {
	t.Helper()
	st := state.NewMemoryStore()
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	cfg := &config.Config{Region: "us-east-1", AccountID: "123456789012"}
	return New(cfg, st, zap.NewNop(), clk), st
}

func mustOK(t *testing.T, what string, aerr *protocol.AWSError) {
	t.Helper()
	if aerr != nil {
		t.Fatalf("%s: %s: %s", what, aerr.Code, aerr.Message)
	}
}

func wantCode(t *testing.T, what string, aerr *protocol.AWSError, code string) {
	t.Helper()
	if aerr == nil {
		t.Fatalf("%s succeeded, want %s", what, code)
	}
	if aerr.Code != code {
		t.Fatalf("%s: code = %s (%s), want %s", what, aerr.Code, aerr.Message, code)
	}
}

func startReq(query string) *startQueryExecReq {
	return &startQueryExecReq{QueryString: query, ResultConfiguration: &ResultConfiguration{OutputLocation: "s3://results/"}}
}

func TestStatementType_classes(t *testing.T) {
	for query, want := range map[string]string{
		"SELECT 1":                               statementDML,
		"  with t as (select 1) select * from t": statementDML,
		"/* note */ INSERT INTO t VALUES (1)":    statementDML,
		"CREATE TABLE t AS\n(SELECT 1)":          statementDML,
		"create external table t (id int)":       statementDDL,
		"MSCK REPAIR TABLE t":                    statementDDL,
		"DROP TABLE t":                           statementDDL,
		"-- c\nDESCRIBE t":                       statementUtility,
		"SHOW CREATE TABLE t":                    statementUtility,
		"EXPLAIN SELECT 1":                       statementUtility,
		"frobnicate":                             statementDML,
	} {
		if got := statementType(query); got != want {
			t.Errorf("statementType(%q) = %s, want %s", query, got, want)
		}
	}
}

func TestApplyConfigurationUpdates_setsAndRemoves(t *testing.T) {
	// Given: a configuration with a cutoff, a location and encryption
	cur := &WorkGroupConfiguration{
		BytesScannedCutoffPerQuery: ptr(int64(20000000)),
		RequesterPaysEnabled:       ptr(true),
		ResultConfiguration: &ResultConfiguration{
			OutputLocation:          "s3://old/",
			EncryptionConfiguration: &EncryptionConfiguration{EncryptionOption: "SSE_S3"},
		},
	}

	// When: the cutoff and encryption are removed and the location moved
	next, aerr := applyConfigurationUpdates(cur, &WorkGroupConfigurationUpdates{
		RemoveBytesScannedCutoffPerQuery: ptr(true),
		ResultConfigurationUpdates: &ResultConfigurationUpdates{
			ResultConfiguration:           ResultConfiguration{OutputLocation: "s3://new/"},
			RemoveEncryptionConfiguration: ptr(true),
		},
	})
	mustOK(t, "applyConfigurationUpdates", aerr)

	// Then: exactly those change, the engine is resolved, and cur is untouched
	switch {
	case next.BytesScannedCutoffPerQuery != nil, !isTrue(next.RequesterPaysEnabled):
		t.Fatalf("scalars = %+v", next)
	case next.ResultConfiguration.OutputLocation != "s3://new/", next.ResultConfiguration.EncryptionConfiguration != nil:
		t.Fatalf("ResultConfiguration = %+v", next.ResultConfiguration)
	case next.EngineVersion.EffectiveEngineVersion != engineVersion3:
		t.Fatalf("EngineVersion = %+v", next.EngineVersion)
	case cur.ResultConfiguration.OutputLocation != "s3://old/", cur.BytesScannedCutoffPerQuery == nil:
		t.Fatalf("the current configuration was modified: %+v", cur)
	}
}

func TestApplyConfigurationUpdates_cutoffBelowMinimum(t *testing.T) {
	_, aerr := applyConfigurationUpdates(nil, &WorkGroupConfigurationUpdates{BytesScannedCutoffPerQuery: ptr(int64(1))})
	wantCode(t, "cutoff below minimum", aerr, codeInvalidRequest)
}

func TestIdempotent_tokenLength(t *testing.T) {
	s, _ := newTestService(t)
	req := startReq("SELECT 1")
	req.ClientRequestToken = "short"
	_, aerr := s.startQueryExecutionTyped(context.Background(), req)
	wantCode(t, "short token", aerr, codeInvalidRequest)
}

func TestStore_malformedRecordsAreIsolated(t *testing.T) {
	// Given: one good query and one corrupt record in primary
	ctx := context.Background()
	s, st := newTestService(t)
	good, aerr := s.startQueryExecutionTyped(ctx, startReq("SELECT 1"))
	mustOK(t, "StartQueryExecution", aerr)
	if err := st.Set(ctx, nsQueries, "corrupt", "{not json"); err != nil {
		t.Fatal(err)
	}

	// When: the workgroup's queries are listed, and the corrupt one read
	list, aerr := s.listQueryExecutionsTyped(ctx, &listQueriesReq{})
	mustOK(t, "ListQueryExecutions", aerr)
	_, getErr := s.getQueryExecutionTyped(ctx, &queryIDReq{QueryExecutionId: "corrupt"})

	// Then: the list holds the good one, and the corrupt one reads as absent
	if len(list.QueryExecutionIds) != 1 || list.QueryExecutionIds[0] != good.QueryExecutionId {
		t.Fatalf("QueryExecutionIds = %v", list.QueryExecutionIds)
	}
	wantCode(t, "GetQueryExecution corrupt", getErr, codeInvalidRequest)
}

func TestStore_legacyQueryRecordRanInPrimary(t *testing.T) {
	// Given: a query stored before workgroups were tracked
	ctx := context.Background()
	s, st := newTestService(t)
	if err := st.Set(ctx, nsQueries, "old", `{"QueryExecutionId":"old","Query":"SELECT 1","Status":{"State":"SUCCEEDED","SubmissionDateTime":1}}`); err != nil {
		t.Fatal(err)
	}

	// When: primary's executions are listed
	list, aerr := s.listQueryExecutionsTyped(ctx, &listQueriesReq{})
	mustOK(t, "ListQueryExecutions", aerr)

	// Then: the old query is among them
	if len(list.QueryExecutionIds) != 1 || list.QueryExecutionIds[0] != "old" {
		t.Fatalf("QueryExecutionIds = %v", list.QueryExecutionIds)
	}
}

func TestStore_legacyWorkGroupGetsAnEngineVersion(t *testing.T) {
	// Given: a workgroup stored before workgroups carried an engine version
	ctx := context.Background()
	s, st := newTestService(t)
	if err := st.Set(ctx, nsWorkGroups, "old", `{"Name":"old","State":"ENABLED","Configuration":{"ResultConfiguration":{"OutputLocation":"s3://r/"}}}`); err != nil {
		t.Fatal(err)
	}

	// When: it is read
	out, aerr := s.getWorkGroupTyped(ctx, &workGroupNameReq{WorkGroup: "old"})
	mustOK(t, "GetWorkGroup", aerr)

	// Then: it reports AUTO, as one created without a choice does, and keeps its settings
	cfg := out.WorkGroup.Configuration
	if cfg.EngineVersion.SelectedEngineVersion != engineVersionAuto || cfg.ResultConfiguration.OutputLocation != "s3://r/" {
		t.Fatalf("Configuration = %+v", cfg)
	}
}
