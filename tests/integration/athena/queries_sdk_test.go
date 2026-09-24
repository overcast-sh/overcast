package athena_test

// queries_sdk_test.go — query executions through the AWS SDK for Go v2: the
// full QueryExecution shape, result configuration resolved against the
// workgroup, ClientRequestToken idempotency, per-workgroup listing, batch get
// and the result-state rules.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestStartQueryExecution_fullExecutionShape(t *testing.T) {
	// Given: a query started with a context, parameters and reuse settings
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	out := must[*athena.StartQueryExecutionOutput](t, "StartQueryExecution")(c.StartQueryExecution(ctx, &athena.StartQueryExecutionInput{
		QueryString:           aws.String("SELECT * FROM events WHERE id = ?"),
		QueryExecutionContext: &types.QueryExecutionContext{Catalog: aws.String("AwsDataCatalog"), Database: aws.String("analytics")},
		ExecutionParameters:   []string{"42"},
		ResultConfiguration:   &types.ResultConfiguration{OutputLocation: aws.String(resultsLocation)},
		ResultReuseConfiguration: &types.ResultReuseConfiguration{
			ResultReuseByAgeConfiguration: &types.ResultReuseByAgeConfiguration{Enabled: true, MaxAgeInMinutes: aws.Int32(30)},
		},
	}))

	// When: it is read back
	qe := must[*athena.GetQueryExecutionOutput](t, "GetQueryExecution")(c.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: out.QueryExecutionId})).QueryExecution

	// Then: every member is there
	switch {
	case qe.Status.State != types.QueryExecutionStateSucceeded, qe.Status.SubmissionDateTime == nil, qe.Status.CompletionDateTime == nil:
		t.Fatalf("Status = %+v", qe.Status)
	case aws.ToString(qe.WorkGroup) != "primary", qe.StatementType != types.StatementTypeDml:
		t.Fatalf("WorkGroup/StatementType = %q/%s", aws.ToString(qe.WorkGroup), qe.StatementType)
	case aws.ToString(qe.QueryExecutionContext.Database) != "analytics", aws.ToString(qe.QueryExecutionContext.Catalog) != "AwsDataCatalog":
		t.Fatalf("QueryExecutionContext = %+v", qe.QueryExecutionContext)
	case len(qe.ExecutionParameters) != 1 || qe.ExecutionParameters[0] != "42":
		t.Fatalf("ExecutionParameters = %v", qe.ExecutionParameters)
	case aws.ToString(qe.ResultConfiguration.OutputLocation) != resultsLocation:
		t.Fatalf("ResultConfiguration = %+v", qe.ResultConfiguration)
	case aws.ToInt32(qe.ResultReuseConfiguration.ResultReuseByAgeConfiguration.MaxAgeInMinutes) != 30:
		t.Fatalf("ResultReuseConfiguration = %+v", qe.ResultReuseConfiguration)
	case aws.ToString(qe.EngineVersion.EffectiveEngineVersion) != "Athena engine version 3":
		t.Fatalf("EngineVersion = %+v", qe.EngineVersion)
	case qe.Statistics == nil || aws.ToInt64(qe.Statistics.DataScannedInBytes) != 0:
		t.Fatalf("Statistics = %+v", qe.Statistics)
	}
}

func TestStartQueryExecution_statementTypes(t *testing.T) {
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	for query, want := range map[string]types.StatementType{
		"CREATE EXTERNAL TABLE t (id int) LOCATION 's3://b/'": types.StatementTypeDdl,
		"CREATE TABLE t2 AS SELECT 1":                         types.StatementTypeDml,
		"-- recent\nSHOW TABLES":                              types.StatementTypeUtility,
		"(SELECT 1)":                                          types.StatementTypeDml,
	} {
		id := startSDKQuery(t, c, "primary", query)
		qe := must[*athena.GetQueryExecutionOutput](t, "GetQueryExecution")(c.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)})).QueryExecution
		if qe.StatementType != want {
			t.Errorf("StatementType(%q) = %s, want %s", query, qe.StatementType, want)
		}
	}
}

func TestStartQueryExecution_noOutputLocation(t *testing.T) {
	// Given: primary, which has no result location
	c := athenaClient(t, helpers.NewTestServer(t))

	// When: a query names none either
	_, err := c.StartQueryExecution(context.Background(), &athena.StartQueryExecutionInput{QueryString: aws.String("SELECT 1")})

	// Then: "Athena issues an error that no output location is provided"
	wantAPIError(t, "StartQueryExecution", err, "InvalidRequestException")
}

func TestStartQueryExecution_workGroupConfigurationResolution(t *testing.T) {
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	for _, tc := range []struct {
		name, want string
		enforce    bool
	}{
		{name: "client-wins", want: "s3://client/"},
		{name: "enforced", enforce: true, want: "s3://workgroup/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a workgroup with its own result location
			must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{
				Name: aws.String(tc.name),
				Configuration: &types.WorkGroupConfiguration{
					EnforceWorkGroupConfiguration: aws.Bool(tc.enforce),
					ResultConfiguration: &types.ResultConfiguration{
						OutputLocation:          aws.String("s3://workgroup/"),
						EncryptionConfiguration: &types.EncryptionConfiguration{EncryptionOption: types.EncryptionOptionSseS3},
					},
				},
			}))

			// When: a query names a different location
			out := must[*athena.StartQueryExecutionOutput](t, "StartQueryExecution")(c.StartQueryExecution(ctx, &athena.StartQueryExecutionInput{
				QueryString:         aws.String("SELECT 1"),
				WorkGroup:           aws.String(tc.name),
				ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String("s3://client/")},
			}))

			// Then: the workgroup decides when it enforces, the client
			// otherwise, and the workgroup's encryption carries over
			rc := must[*athena.GetQueryExecutionOutput](t, "GetQueryExecution")(c.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: out.QueryExecutionId})).QueryExecution.ResultConfiguration
			if aws.ToString(rc.OutputLocation) != tc.want || rc.EncryptionConfiguration == nil {
				t.Fatalf("ResultConfiguration = %+v, want OutputLocation %s with the workgroup's encryption", rc, tc.want)
			}
		})
	}
}

func TestStartQueryExecution_disabledWorkGroup(t *testing.T) {
	// Given: a disabled workgroup
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String("off")}))
	must[*athena.UpdateWorkGroupOutput](t, "UpdateWorkGroup")(c.UpdateWorkGroup(ctx, &athena.UpdateWorkGroupInput{WorkGroup: aws.String("off"), State: types.WorkGroupStateDisabled}))

	// When: a query is started in it
	_, err := c.StartQueryExecution(ctx, &athena.StartQueryExecutionInput{
		QueryString: aws.String("SELECT 1"), WorkGroup: aws.String("off"),
		ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String(resultsLocation)},
	})

	// Then: it is refused
	wantAPIError(t, "StartQueryExecution", err, "InvalidRequestException")
}

func TestStartQueryExecution_clientRequestToken(t *testing.T) {
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	token := strings.Repeat("t", 40)
	start := func(query string) (*athena.StartQueryExecutionOutput, error) {
		return c.StartQueryExecution(ctx, &athena.StartQueryExecutionInput{
			QueryString: aws.String(query), ClientRequestToken: aws.String(token),
			ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String(resultsLocation)},
		})
	}

	// Given: a query started with a token
	first := must[*athena.StartQueryExecutionOutput](t, "first start")(start("SELECT 1"))

	// When: the same request is retried, and the token reused for another
	again := must[*athena.StartQueryExecutionOutput](t, "retry")(start("SELECT 1"))
	_, changed := start("SELECT 2")

	// Then: the retry returns the same query, the reuse is refused, and only
	// one query exists
	if aws.ToString(again.QueryExecutionId) != aws.ToString(first.QueryExecutionId) {
		t.Fatalf("retry id = %s, want %s", aws.ToString(again.QueryExecutionId), aws.ToString(first.QueryExecutionId))
	}
	wantAPIError(t, "token reuse", changed, "InvalidRequestException")
	list := must[*athena.ListQueryExecutionsOutput](t, "ListQueryExecutions")(c.ListQueryExecutions(ctx, &athena.ListQueryExecutionsInput{}))
	if len(list.QueryExecutionIds) != 1 {
		t.Fatalf("QueryExecutionIds = %v, want one", list.QueryExecutionIds)
	}
}

func TestListQueryExecutions_perWorkGroupNewestFirstPaged(t *testing.T) {
	// Given: three queries in primary and one elsewhere, a clock tick apart
	ctx := context.Background()
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	c := athenaClient(t, srv)
	must[*athena.CreateWorkGroupOutput](t, "CreateWorkGroup")(c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{Name: aws.String("other")}))
	var ids []string
	for i := range 3 {
		ids = append(ids, startSDKQuery(t, c, "primary", "SELECT "+string(rune('1'+i))))
		srv.AdvanceClock(time.Second)
	}
	startSDKQuery(t, c, "other", "SELECT 9")

	// When: primary's executions are listed two at a time
	var got []string
	p := athena.NewListQueryExecutionsPaginator(c, &athena.ListQueryExecutionsInput{MaxResults: aws.Int32(2)})
	for p.HasMorePages() {
		got = append(got, must[*athena.ListQueryExecutionsOutput](t, "ListQueryExecutions")(p.NextPage(ctx)).QueryExecutionIds...)
	}

	// Then: primary's three, most recent first
	if len(got) != 3 || got[0] != ids[2] || got[1] != ids[1] || got[2] != ids[0] {
		t.Fatalf("ids = %v, want %v reversed", got, ids)
	}
}

func TestBatchGetQueryExecution_unknownIdsAreUnprocessed(t *testing.T) {
	// Given: one query
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	id := startSDKQuery(t, c, "primary", "SELECT 1")

	// When: it and an unknown ID are fetched together
	out := must[*athena.BatchGetQueryExecutionOutput](t, "BatchGetQueryExecution")(c.BatchGetQueryExecution(ctx, &athena.BatchGetQueryExecutionInput{
		QueryExecutionIds: []string{id, "missing"},
	}))

	// Then: one comes back, the other is unprocessed
	if len(out.QueryExecutions) != 1 || aws.ToString(out.QueryExecutions[0].QueryExecutionId) != id {
		t.Fatalf("QueryExecutions = %+v", out.QueryExecutions)
	}
	if len(out.UnprocessedQueryExecutionIds) != 1 || aws.ToString(out.UnprocessedQueryExecutionIds[0].QueryExecutionId) != "missing" ||
		aws.ToString(out.UnprocessedQueryExecutionIds[0].ErrorCode) == "" {
		t.Fatalf("UnprocessedQueryExecutionIds = %+v", out.UnprocessedQueryExecutionIds)
	}
}

func TestGetQueryResults_succeededQueryIsEmpty(t *testing.T) {
	// Given: a finished query
	ctx := context.Background()
	c := athenaClient(t, helpers.NewTestServer(t))
	id := startSDKQuery(t, c, "primary", "SELECT 1")

	// When: its results are read
	out := must[*athena.GetQueryResultsOutput](t, "GetQueryResults")(c.GetQueryResults(ctx, &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id)}))

	// Then: an empty result set
	if len(out.ResultSet.Rows) != 0 || out.ResultSet.ResultSetMetadata == nil {
		t.Fatalf("ResultSet = %+v", out.ResultSet)
	}
}
