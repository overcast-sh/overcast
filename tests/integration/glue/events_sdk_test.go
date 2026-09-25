package glue_test

// events_sdk_test.go — the bus events Glue publishes for table and partition
// changes, each read back as the call that caused it published it.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/glue/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

type glueTableEvent struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	ARN      string `json:"arn"`
	Change   string `json:"change"`
}

// oneGlueEvent asserts that a call published exactly one event of type typ
// and returns its payload.
func oneGlueEvent(t *testing.T, got []helpers.BusEvent, typ string) glueTableEvent {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s published %d times, want once: %+v", typ, len(got), got)
	}
	var p glueTableEvent
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("%s payload: %v", typ, err)
	}
	if got[0].Source != "glue" || got[0].ResourceARN != p.ARN {
		t.Fatalf("%s: source %q, resource ARN %q, payload ARN %q", typ, got[0].Source, got[0].ResourceARN, p.ARN)
	}
	return p
}

func TestTableAndPartitionEvents_eachCallPublishesOnce(t *testing.T) {
	// Given: a database
	srv := helpers.NewTestServer(t)
	c := glueClient(t, srv)
	ctx := context.Background()
	must[*glue.CreateDatabaseOutput](t, "CreateDatabase")(c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &types.DatabaseInput{Name: aws.String("web")},
	}))
	want := glueTableEvent{Database: "web", Table: "clicks", ARN: "arn:aws:glue:us-east-1:000000000000:table/web/clicks"}

	// When: a table is created
	created := must[*glue.CreateTableOutput](t, "CreateTable")(c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String("web"), TableInput: eventsTableInput("clicks"),
	}))

	// Then: the call published one TableChanged, naming the table
	want.Change = "created"
	if got := oneGlueEvent(t, helpers.EventsOfCall(t, srv, created.ResultMetadata, "glue:TableChanged"), "TableChanged"); got != want {
		t.Fatalf("TableChanged = %+v, want %+v", got, want)
	}

	// When: two partitions are added in one batch
	added := must[*glue.BatchCreatePartitionOutput](t, "BatchCreatePartition")(c.BatchCreatePartition(ctx, &glue.BatchCreatePartitionInput{
		DatabaseName: aws.String("web"), TableName: aws.String("clicks"),
		PartitionInputList: []types.PartitionInput{{Values: []string{"2026", "09"}}, {Values: []string{"2026", "10"}}},
	}))

	// Then: the call published one PartitionsChanged for the table
	want.Change = ""
	if got := oneGlueEvent(t, helpers.EventsOfCall(t, srv, added.ResultMetadata, "glue:PartitionsChanged"), "PartitionsChanged"); got != want {
		t.Fatalf("PartitionsChanged = %+v, want %+v", got, want)
	}

	// When: the table is deleted
	deleted := must[*glue.DeleteTableOutput](t, "DeleteTable")(c.DeleteTable(ctx, &glue.DeleteTableInput{
		DatabaseName: aws.String("web"), Name: aws.String("clicks"),
	}))

	// Then: the call published one TableChanged for the delete
	want.Change = "deleted"
	if got := oneGlueEvent(t, helpers.EventsOfCall(t, srv, deleted.ResultMetadata, "glue:TableChanged"), "TableChanged"); got != want {
		t.Fatalf("TableChanged = %+v, want %+v", got, want)
	}
}
