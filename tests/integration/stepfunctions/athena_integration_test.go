package stepfunctions_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The optimized Athena integrations, against Athena's inert engine: without
// Docker a query succeeds at once with no rows, and Hive DDL still runs
// against the Glue catalog — so a malformed DDL statement is a query that
// fails, with no container involved.

const athenaResults = "s3://athena-results/sfn/"

func athenaSDKClient(srv *helpers.TestServer) *athena.Client {
	return athena.New(athena.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

// athenaQueryTask is a Task state on resource whose Parameters start query.
func athenaQueryTask(resource, query, next string) string {
	transition := `"End":true`
	if next != "" {
		transition = `"Next":"` + next + `"`
	}
	return `{"Type":"Task","Resource":"arn:aws:states:::athena:` + resource + `",
	  "Parameters":{"QueryString":"` + query + `","WorkGroup":"primary",
	    "ResultConfiguration":{"OutputLocation":"` + athenaResults + `"}},` + transition + `}`
}

func decodeExecutionOutput(t *testing.T, got describeExecutionResult, v any) {
	t.Helper()
	if got.Status != "SUCCEEDED" {
		t.Fatalf("status = %q (%s: %s)", got.Status, got.Error, got.Cause)
	}
	if err := json.Unmarshal([]byte(got.Output), v); err != nil {
		t.Fatalf("output %q: %v", got.Output, err)
	}
}

func TestStartExecution_athenaStartQueryExecutionRequestResponse(t *testing.T) {
	// Given: a request-response startQueryExecution Task
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Q","States":{"Q":` + athenaQueryTask("startQueryExecution", "SELECT 1", "") + `}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "athena-start", def, `{}`)

	// Then: the result is the StartQueryExecution response, naming a query
	// Athena knows
	var out map[string]any
	decodeExecutionOutput(t, got, &out)
	id, _ := out["QueryExecutionId"].(string)
	if id == "" || len(out) != 1 {
		t.Fatalf("output = %s, want only QueryExecutionId", got.Output)
	}
	qe, err := athenaSDKClient(srv).GetQueryExecution(context.Background(), &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)})
	if err != nil {
		t.Fatalf("GetQueryExecution(%s): %v", id, err)
	}
	if q := aws.ToString(qe.QueryExecution.Query); q != "SELECT 1" {
		t.Errorf("query = %q, want SELECT 1", q)
	}
}

func TestStartExecution_athenaStartQueryExecutionSyncThenResults(t *testing.T) {
	// Given: a .sync query whose id feeds getQueryExecution and getQueryResults
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Run","States":{
	  "Run":` + athenaQueryTask("startQueryExecution.sync", "SELECT 1", "Describe") + `,
	  "Describe":{"Type":"Task","Resource":"arn:aws:states:::athena:getQueryExecution",
	    "Parameters":{"QueryExecutionId.$":"$.QueryExecution.QueryExecutionId"},
	    "ResultPath":"$.Described","Next":"Results"},
	  "Results":{"Type":"Task","Resource":"arn:aws:states:::athena:getQueryResults",
	    "Parameters":{"QueryExecutionId.$":"$.QueryExecution.QueryExecutionId"},
	    "ResultPath":"$.Results","End":true}}}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "athena-sync", def, `{}`)

	// Then: .sync hands on the final GetQueryExecution response, and the
	// two request-response calls return their API responses
	var out struct {
		QueryExecution struct {
			QueryExecutionId string
			Status           struct{ State string }
		}
		Described struct {
			QueryExecution struct{ QueryExecutionId string }
		}
		Results struct {
			ResultSet *struct{ Rows []any }
		}
	}
	decodeExecutionOutput(t, got, &out)
	if out.QueryExecution.QueryExecutionId == "" || out.QueryExecution.Status.State != "SUCCEEDED" {
		t.Errorf("sync output = %s, want the SUCCEEDED QueryExecution", got.Output)
	}
	if out.Described.QueryExecution.QueryExecutionId != out.QueryExecution.QueryExecutionId {
		t.Errorf("getQueryExecution output = %s, want the same query", got.Output)
	}
	if out.Results.ResultSet == nil {
		t.Errorf("getQueryResults output = %s, want a ResultSet", got.Output)
	}
	succeeded := 0
	for _, e := range execHistory(t, srv, execARN) {
		if e.Type == "TaskSucceeded" {
			succeeded++
		}
	}
	if succeeded != 3 {
		t.Errorf("TaskSucceeded events = %d, want 3", succeeded)
	}
}

func TestStartExecution_athenaStartQueryExecutionSyncFailedQuery(t *testing.T) {
	// Given: a .sync Task on a statement Athena cannot parse
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Run","States":{"Run":` + athenaQueryTask("startQueryExecution.sync", "CREATE EXTERNAL TABLE", "") + `}}`

	// When: it runs
	got, execARN := runToEnd(t, srv, "athena-sync-failed", def, `{}`)

	// Then: the Task fails with States.TaskFailed, its cause the FAILED
	// GetQueryExecution response
	if got.Status != "FAILED" || got.Error != "States.TaskFailed" {
		t.Fatalf("status = %q, error = %q (%s), want FAILED with States.TaskFailed", got.Status, got.Error, got.Cause)
	}
	var cause struct {
		QueryExecution struct {
			Status struct{ State, StateChangeReason string }
		}
	}
	if err := json.Unmarshal([]byte(got.Cause), &cause); err != nil {
		t.Fatalf("cause %q is not the QueryExecution JSON: %v", got.Cause, err)
	}
	if cause.QueryExecution.Status.State != string(athenatypes.QueryExecutionStateFailed) || cause.QueryExecution.Status.StateChangeReason == "" {
		t.Errorf("cause = %s, want the FAILED QueryExecution with its reason", got.Cause)
	}
	var failed *historyEvent
	for _, e := range execHistory(t, srv, execARN) {
		if e.Type == "TaskFailed" {
			failed = &e
		}
	}
	if failed == nil || failed.TaskFailedEventDetails.ResourceType != "athena" || failed.TaskFailedEventDetails.Resource != "startQueryExecution.sync" {
		t.Errorf("TaskFailed event = %+v, want resource athena startQueryExecution.sync", failed)
	}
}

func TestStartExecution_athenaStopQueryExecution(t *testing.T) {
	// Given: a query Athena has already run
	srv := helpers.NewTestServer(t)
	client := athenaSDKClient(srv)
	started, err := client.StartQueryExecution(context.Background(), &athena.StartQueryExecutionInput{
		QueryString:         aws.String("SELECT 1"),
		ResultConfiguration: &athenatypes.ResultConfiguration{OutputLocation: aws.String(athenaResults)},
	})
	if err != nil {
		t.Fatalf("StartQueryExecution: %v", err)
	}
	def := `{"StartAt":"Stop","States":{"Stop":{"Type":"Task","Resource":"arn:aws:states:::athena:stopQueryExecution",
	  "Parameters":{"QueryExecutionId.$":"$.id"},"End":true}}}`

	// When: a stopQueryExecution Task names it
	got, _ := runToEnd(t, srv, "athena-stop", def, `{"id":"`+aws.ToString(started.QueryExecutionId)+`"}`)

	// Then: the Task succeeds with StopQueryExecution's empty response
	var out map[string]any
	decodeExecutionOutput(t, got, &out)
	if len(out) != 0 {
		t.Errorf("output = %s, want {}", got.Output)
	}
}

func TestStartExecution_athenaUnknownQueryFailsWithAthenaError(t *testing.T) {
	// Given: a getQueryExecution Task naming no query
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Get","States":{"Get":{"Type":"Task","Resource":"arn:aws:states:::athena:getQueryExecution",
	  "Parameters":{"QueryExecutionId":"00000000-0000-0000-0000-000000000000"},"End":true}}}`

	// When: it runs
	got, _ := runToEnd(t, srv, "athena-unknown", def, `{}`)

	// Then: Athena's error fails the Task under the Athena. prefix
	if got.Status != "FAILED" || got.Error != "Athena.InvalidRequestException" {
		t.Errorf("status = %q, error = %q (%s), want FAILED with Athena.InvalidRequestException", got.Status, got.Error, got.Cause)
	}
}

func TestCreateStateMachine_athenaIntegrationAWSDoesNotOffer(t *testing.T) {
	// Given: a Task on athena:getNamedQuery, which is not an optimized integration
	srv := helpers.NewTestServer(t)
	def := `{"StartAt":"Q","States":{"Q":{"Type":"Task","Resource":"arn:aws:states:::athena:getNamedQuery","End":true}}}`

	// When: a state machine is created with it
	resp := sfnCall(t, srv, "CreateStateMachine", map[string]any{
		"name": "athena-named", "definition": def, "roleArn": "arn:aws:iam::000000000000:role/r",
	})
	defer resp.Body.Close()

	// Then: it is refused as an unrecognized resource
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidDefinition")
}
