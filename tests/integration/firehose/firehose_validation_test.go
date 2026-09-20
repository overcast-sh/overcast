// Request validation for Firehose, against the constraints the pinned Smithy
// model states (models/firehose/service/2015-08-04/firehose-2015-08-04.json):
//
//	DeliveryStreamName  @length(1, 64), @pattern ^[a-zA-Z0-9_.-]+$
//	Data (blob)         @length(0, 1024000)
//	Records list        @length(1, 500)
//
// and against the documented PutRecordBatch quota of 4 MiB per call. Every
// exception in the Firehose model is a client error with no httpError
// override, so each of these is HTTP 400 — including ResourceNotFoundException,
// which is why the not-found assertions here say 400 rather than 404.
package firehose_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// record builds one PutRecord/PutRecordBatch entry of n bytes of data.
func record(n int) map[string]any {
	return map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", n)))}
}

func records(count, size int) []map[string]any {
	out := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, record(size))
	}
	return out
}

// ─── CreateDeliveryStream ─────────────────────────────────────────────────────

func TestCreateDeliveryStream_duplicateName(t *testing.T) {
	// Given: a delivery stream already exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "dupe-stream")

	// When: CreateDeliveryStream is called again with the same name
	resp := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "dupe-stream",
	})
	defer resp.Body.Close()

	// Then: ResourceInUseException, and the first stream is untouched
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceInUseException")
}

func TestCreateDeliveryStream_duplicateNameKeepsOriginal(t *testing.T) {
	// Given: a delivery stream created as KinesisStreamAsSource
	srv := helpers.NewTestServer(t)
	first := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "keep-me",
		"DeliveryStreamType": "KinesisStreamAsSource",
	})
	defer first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)

	// When: a second create with the same name but a different type is refused
	dup := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "keep-me",
		"DeliveryStreamType": "DirectPut",
	})
	defer dup.Body.Close()
	helpers.AssertStatus(t, dup, http.StatusBadRequest)

	// Then: the stored stream still carries the original type
	desc := fhCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "keep-me",
	})
	defer desc.Body.Close()
	helpers.AssertStatus(t, desc, http.StatusOK)
	var result struct {
		DeliveryStreamDescription struct {
			DeliveryStreamType string `json:"DeliveryStreamType"`
		} `json:"DeliveryStreamDescription"`
	}
	helpers.DecodeJSON(t, desc, &result)
	if got := result.DeliveryStreamDescription.DeliveryStreamType; got != "KinesisStreamAsSource" {
		t.Errorf("expected the first stream to survive with DeliveryStreamType=KinesisStreamAsSource, got %q", got)
	}
}

func TestCreateDeliveryStream_nameTooLong(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: CreateDeliveryStream is called with a 65-character name
	resp := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": strings.Repeat("a", 65),
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestCreateDeliveryStream_nameIllegalCharacter(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: CreateDeliveryStream is called with a name outside the pattern
	resp := fhCall(t, srv, "CreateDeliveryStream", map[string]any{
		"DeliveryStreamName": "bad name!",
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException, and nothing was stored
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")

	list := fhCall(t, srv, "ListDeliveryStreams", map[string]any{})
	defer list.Body.Close()
	var listed struct {
		DeliveryStreamNames []string `json:"DeliveryStreamNames"`
	}
	helpers.DecodeJSON(t, list, &listed)
	if len(listed.DeliveryStreamNames) != 0 {
		t.Errorf("expected no delivery streams, got %v", listed.DeliveryStreamNames)
	}
}

// ─── PutRecord ────────────────────────────────────────────────────────────────

func TestPutRecord_missingRecord(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecord is called without the required Record member
	resp := fhCall(t, srv, "PutRecord", map[string]any{
		"DeliveryStreamName": "test-stream",
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestPutRecord_dataTooLarge(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecord carries one byte more than the 1,024,000-byte Data cap
	resp := fhCall(t, srv, "PutRecord", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Record":             record(1024001),
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestPutRecord_dataAtLimit(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecord carries exactly the maximum Data size
	resp := fhCall(t, srv, "PutRecord", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Record":             record(1024000),
	})
	defer resp.Body.Close()

	// Then: it is accepted
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestPutRecord_unknownStream(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: PutRecord names a stream that does not exist
	resp := fhCall(t, srv, "PutRecord", map[string]any{
		"DeliveryStreamName": "missing",
		"Record":             record(5),
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException with HTTP 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── PutRecordBatch ───────────────────────────────────────────────────────────

func TestPutRecordBatch_emptyRecords(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecordBatch is called with an empty Records list
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            []map[string]any{},
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestPutRecordBatch_tooManyRecords(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecordBatch carries 501 records
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            records(501, 1),
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestPutRecordBatch_recordDataTooLarge(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: one entry exceeds the per-record Data cap
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            []map[string]any{record(10), record(1024001)},
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestPutRecordBatch_batchTooLarge(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: five 1,000,000-byte records stay inside both the count and the
	// per-record limits but together exceed the 4 MiB per-call quota
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            records(5, 1000000),
	})
	defer resp.Body.Close()

	// Then: InvalidArgumentException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestPutRecordBatch_undecodableRecordData(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: Record.Data is not base64 — the wire form of a blob member
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            []map[string]any{{"Data": "not base64!!"}},
	})
	defer resp.Body.Close()

	// Then: the decoder refuses it rather than acknowledging a record it never
	// read — this used to answer 200 with an empty RequestResponses list
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "SerializationException")
}

func TestPutRecordBatch_recordsNotAList(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: Records is a string rather than a list of Record structures
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            "nonsense",
	})
	defer resp.Body.Close()

	// Then: the decoder refuses it rather than acknowledging zero results
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "SerializationException")
}

func TestPutRecordBatch_success(t *testing.T) {
	// Given: a delivery stream exists
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "test-stream")

	// When: PutRecordBatch is called with two records
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "test-stream",
		"Records":            records(2, 8),
	})
	defer resp.Body.Close()

	// Then: one RecordId per record and FailedPutCount 0
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		FailedPutCount   int `json:"FailedPutCount"`
		RequestResponses []struct {
			RecordId string `json:"RecordId"`
		} `json:"RequestResponses"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.FailedPutCount != 0 {
		t.Errorf("expected FailedPutCount=0, got %d", result.FailedPutCount)
	}
	if len(result.RequestResponses) != 2 {
		t.Fatalf("expected 2 RequestResponses, got %d", len(result.RequestResponses))
	}
	for i, rr := range result.RequestResponses {
		if rr.RecordId == "" {
			t.Errorf("RequestResponses[%d] has an empty RecordId", i)
		}
	}
}

func TestPutRecordBatch_unknownStream(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: PutRecordBatch names a stream that does not exist
	resp := fhCall(t, srv, "PutRecordBatch", map[string]any{
		"DeliveryStreamName": "missing",
		"Records":            records(1, 4),
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException with HTTP 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── Not-found status ─────────────────────────────────────────────────────────

func TestDescribeDeliveryStream_unknownStream(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: DescribeDeliveryStream names a stream that does not exist
	resp := fhCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "missing",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException with HTTP 400, not 404
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDeleteDeliveryStream_unknownStream(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: DeleteDeliveryStream names a stream that does not exist
	resp := fhCall(t, srv, "DeleteDeliveryStream", map[string]any{
		"DeliveryStreamName": "missing",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException with HTTP 400, not 404
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDescribeDeliveryStream_illegalName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: DescribeDeliveryStream is given a name outside the pattern
	resp := fhCall(t, srv, "DescribeDeliveryStream", map[string]any{
		"DeliveryStreamName": "arn:aws:firehose:us-east-1:000000000000:deliverystream/x",
	})
	defer resp.Body.Close()

	// Then: the name is rejected before the lookup, as AWS validates it
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

// ─── Tag operations ───────────────────────────────────────────────────────────

func TestTagOperations_unknownStream(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When/Then: each tag operation on an unknown stream reports
	// ResourceNotFoundException with HTTP 400, the same as the rest of the
	// service — the legacy JSON handlers used to answer 404 here while the
	// CBOR path answered 400.
	for _, op := range []struct {
		name string
		body map[string]any
	}{
		{"TagDeliveryStream", map[string]any{
			"DeliveryStreamName": "missing",
			"Tags":               []map[string]any{{"Key": "env", "Value": "dev"}},
		}},
		{"UntagDeliveryStream", map[string]any{
			"DeliveryStreamName": "missing",
			"TagKeys":            []string{"env"},
		}},
		{"ListTagsForDeliveryStream", map[string]any{"DeliveryStreamName": "missing"}},
	} {
		t.Run(op.name, func(t *testing.T) {
			resp := fhCall(t, srv, op.name, op.body)
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
		})
	}
}

func TestDeleteDeliveryStream_illegalName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: DeleteDeliveryStream is given an ARN rather than a name
	resp := fhCall(t, srv, "DeleteDeliveryStream", map[string]any{
		"DeliveryStreamName": "arn:aws:firehose:us-east-1:000000000000:deliverystream/x",
	})
	defer resp.Body.Close()

	// Then: the name is rejected before the lookup, like the other operations.
	// CloudFormation's delivery-stream teardown used to depend on this call
	// tolerating an ARN, because the resource's physical ID was the ARN — see
	// tests/integration/cloudformation/firehose_delivery_stream_test.go.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}
