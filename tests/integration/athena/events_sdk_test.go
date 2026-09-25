package athena_test

// events_sdk_test.go — the athena:QueryStateChanged events a query execution
// publishes as it moves through its states.

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

type queryStateEvent struct {
	QueryExecutionID string `json:"queryExecutionId"`
	WorkGroup        string `json:"workGroup"`
	State            string `json:"state"`
	Database         string `json:"database"`
}

func TestQueryStateChanged_publishedForEachStateTheQueryReaches(t *testing.T) {
	// Given: a query started in the database sales, which the inert engine
	// runs to completion before the call returns
	srv := helpers.NewTestServer(t)
	c := athenaClient(t, srv)
	out := must[*athena.StartQueryExecutionOutput](t, "StartQueryExecution")(c.StartQueryExecution(context.Background(), &athena.StartQueryExecutionInput{
		QueryString:           aws.String("SELECT 1"),
		QueryExecutionContext: &types.QueryExecutionContext{Database: aws.String("sales")},
		ResultConfiguration:   &types.ResultConfiguration{OutputLocation: aws.String(resultsLocation)},
	}))

	// Then: the call published QUEUED and then SUCCEEDED, once each, for
	// this query in the primary workgroup
	var states []string
	for _, e := range helpers.EventsOfCall(t, srv, out.ResultMetadata, "athena:QueryStateChanged") {
		var p queryStateEvent
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if p.QueryExecutionID != aws.ToString(out.QueryExecutionId) || p.WorkGroup != "primary" || p.Database != "sales" || e.Source != "athena" {
			t.Fatalf("event = %+v, payload %+v", e, p)
		}
		states = append(states, p.State)
	}
	if want := []string{"QUEUED", "SUCCEEDED"}; !slices.Equal(states, want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
}
