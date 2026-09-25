package s3tables_test

// events_sdk_test.go — the bus events S3 Tables publishes, for the console to
// follow tables live, each read back as the call that caused it published it.

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3tables"

	"github.com/overcast-sh/overcast/tests/helpers"
)

type tableEvent struct {
	ARN                      string `json:"arn"`
	Bucket                   string `json:"bucket"`
	Namespace                string `json:"namespace"`
	Name                     string `json:"name"`
	PreviousMetadataLocation string `json:"previousMetadataLocation"`
	MetadataLocation         string `json:"metadataLocation"`
}

// oneTableEvent asserts that a call published exactly one event of type typ
// and returns its payload.
func oneTableEvent(t *testing.T, got []helpers.BusEvent, typ string) tableEvent {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s published %d times, want once: %+v", typ, len(got), got)
	}
	var p tableEvent
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("%s payload: %v", typ, err)
	}
	if got[0].Source != "s3tables" || got[0].ResourceARN != p.ARN {
		t.Fatalf("%s: source %q, resource ARN %q, payload ARN %q", typ, got[0].Source, got[0].ResourceARN, p.ARN)
	}
	return p
}

func TestTableEvents_createCommitAndDeleteEachPublishOnce(t *testing.T) {
	// Given: a table bucket with a namespace
	f := newFixture(t, "events-bucket")

	// When: a table is created
	created := f.createTable(t, "orders", nil)

	// Then: the call published TableCreated, naming the table
	p := oneTableEvent(t, helpers.EventsOfCall(t, f.srv, created.ResultMetadata, "s3tables:TableCreated"), "TableCreated")
	if p.ARN != aws.ToString(created.TableARN) || p.Bucket != "events-bucket" || p.Namespace != f.namespace || p.Name != "orders" {
		t.Fatalf("TableCreated = %+v", p)
	}

	// When: a client commits a metadata file
	loc, err := f.tables.GetTableMetadataLocation(f.ctx, &s3tables.GetTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
	})
	if err != nil {
		t.Fatalf("GetTableMetadataLocation: %v", err)
	}
	next := aws.ToString(loc.WarehouseLocation) + "/metadata/00001-abc.metadata.json"
	updated, err := f.tables.UpdateTableMetadataLocation(f.ctx, &s3tables.UpdateTableMetadataLocationInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
		VersionToken: loc.VersionToken, MetadataLocation: aws.String(next),
	})
	if err != nil {
		t.Fatalf("UpdateTableMetadataLocation: %v", err)
	}

	// Then: the call published TableCommitted with the pointer's move
	p = oneTableEvent(t, helpers.EventsOfCall(t, f.srv, updated.ResultMetadata, "s3tables:TableCommitted"), "TableCommitted")
	if p.PreviousMetadataLocation != "" || p.MetadataLocation != next {
		t.Fatalf("TableCommitted = %+v, want no previous file and %s", p, next)
	}

	// When: the table is deleted. Its 204 answer names the request in S3's
	// header, which the S3 Tables SDK does not read, so the test names it.
	const deleteRequestID = "delete-orders"
	_, err = f.tables.DeleteTable(f.ctx, &s3tables.DeleteTableInput{
		TableBucketARN: aws.String(f.bucketARN), Namespace: aws.String(f.namespace), Name: aws.String("orders"),
	}, func(o *s3tables.Options) { o.APIOptions = append(o.APIOptions, helpers.PinRequestID(deleteRequestID)) })
	if err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}

	// Then: the call published TableDeleted
	if p = oneTableEvent(t, helpers.EventsOfRequest(t, f.srv, deleteRequestID, "s3tables:TableDeleted"), "TableDeleted"); p.ARN != aws.ToString(created.TableARN) {
		t.Fatalf("TableDeleted = %+v", p)
	}
}
