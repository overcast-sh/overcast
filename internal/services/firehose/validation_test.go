package firehose

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// The typed operations below are what the RPCv2 CBOR path invokes directly and
// what the JSON 1.0/1.1 handlers delegate to, so asserting on them covers the
// validation on every protocol Firehose answers.
func newTestService(t *testing.T) *Service {
	t.Helper()
	return New(&config.Config{Region: "us-east-1", AccountID: "123456789012"},
		state.NewMemoryStore(), zap.NewNop(), clock.New())
}

func recordOf(n int) *firehoseRecord {
	return &firehoseRecord{Data: []byte(strings.Repeat("x", n))}
}

func assertAWSError(t *testing.T, aerr *protocol.AWSError, code string, status int) {
	t.Helper()
	if aerr == nil {
		t.Fatalf("expected %s, got no error", code)
	}
	if aerr.Code != code {
		t.Errorf("expected code %q, got %q (message: %s)", code, aerr.Code, aerr.Message)
	}
	if aerr.HTTPStatus != status {
		t.Errorf("expected HTTP %d, got %d", status, aerr.HTTPStatus)
	}
}

func TestCreateDeliveryStreamTyped_duplicateName(t *testing.T) {
	// Given: a delivery stream exists
	s := newTestService(t)
	ctx := context.Background()
	if _, aerr := s.createDeliveryStreamTyped(ctx, &createDeliveryStreamReq{DeliveryStreamName: "s1"}); aerr != nil {
		t.Fatalf("first create: %v", aerr)
	}

	// When: the same name is created again
	_, aerr := s.createDeliveryStreamTyped(ctx, &createDeliveryStreamReq{DeliveryStreamName: "s1"})

	// Then: ResourceInUseException, 400
	assertAWSError(t, aerr, "ResourceInUseException", 400)
}

func TestValidateDeliveryStreamName(t *testing.T) {
	// Given: the model's @length(1, 64) and ^[a-zA-Z0-9_.-]+$ constraints
	cases := map[string]struct {
		name    string
		wantErr bool
	}{
		"empty":            {"", true},
		"too long":         {strings.Repeat("a", 65), true},
		"at max length":    {strings.Repeat("a", 64), false},
		"space":            {"bad name", true},
		"slash":            {"a/b", true},
		"arn":              {"arn:aws:firehose:us-east-1:000000000000:deliverystream/x", true},
		"dots and dashes":  {"my.stream-1_2", false},
		"single character": {"a", false},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			// When: the name is validated
			aerr := validateDeliveryStreamName(tc.name)

			// Then: it is accepted or reported as InvalidArgumentException
			switch {
			case tc.wantErr:
				assertAWSError(t, aerr, "InvalidArgumentException", 400)
			case aerr != nil:
				t.Errorf("expected %q to be valid, got %s: %s", tc.name, aerr.Code, aerr.Message)
			}
		})
	}
}

func TestPutRecordTyped_recordConstraints(t *testing.T) {
	// Given: a delivery stream exists
	s := newTestService(t)
	ctx := context.Background()
	if _, aerr := s.createDeliveryStreamTyped(ctx, &createDeliveryStreamReq{DeliveryStreamName: "s1"}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}

	cases := map[string]struct {
		rec     *firehoseRecord
		wantErr bool
	}{
		"missing record": {nil, true},
		"empty data":     {recordOf(0), false},
		"at the cap":     {recordOf(maxRecordDataBytes), false},
		"over the cap":   {recordOf(maxRecordDataBytes + 1), true},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			// When: PutRecord is invoked
			_, aerr := s.putRecordTyped(ctx, &putRecordReq{DeliveryStreamName: "s1", Record: tc.rec})

			// Then: it is accepted or reported as InvalidArgumentException
			switch {
			case tc.wantErr:
				assertAWSError(t, aerr, "InvalidArgumentException", 400)
			case aerr != nil:
				t.Errorf("expected acceptance, got %s: %s", aerr.Code, aerr.Message)
			}
		})
	}
}

func TestPutRecordBatchTyped_batchConstraints(t *testing.T) {
	// Given: a delivery stream exists
	s := newTestService(t)
	ctx := context.Background()
	if _, aerr := s.createDeliveryStreamTyped(ctx, &createDeliveryStreamReq{DeliveryStreamName: "s1"}); aerr != nil {
		t.Fatalf("create: %v", aerr)
	}
	batch := func(count, size int) []firehoseRecord {
		out := make([]firehoseRecord, count)
		for i := range out {
			out[i] = *recordOf(size)
		}
		return out
	}

	cases := map[string]struct {
		records []firehoseRecord
		wantErr bool
	}{
		"nil":              {nil, true},
		"empty":            {[]firehoseRecord{}, true},
		"one":              {batch(1, 4), false},
		"at the count cap": {batch(maxRecordsPerBatch, 1), false},
		"over the count":   {batch(maxRecordsPerBatch+1, 1), true},
		"oversized entry":  {[]firehoseRecord{*recordOf(maxRecordDataBytes + 1)}, true},
		"over 4 MiB total": {batch(5, 1000000), true},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			// When: PutRecordBatch is invoked
			resp, aerr := s.putRecordBatchTyped(ctx, &putRecordBatchReq{DeliveryStreamName: "s1", Records: tc.records})

			// Then: it is refused, or answered with one record id per entry
			switch {
			case tc.wantErr:
				assertAWSError(t, aerr, "InvalidArgumentException", 400)
			case aerr != nil:
				t.Errorf("expected acceptance, got %s: %s", aerr.Code, aerr.Message)
			case len(resp.RequestResponses) != len(tc.records):
				t.Errorf("expected %d RequestResponses, got %d", len(tc.records), len(resp.RequestResponses))
			}
		})
	}
}

func TestTypedOps_unknownStreamIs400(t *testing.T) {
	// Given: an empty store
	s := newTestService(t)
	ctx := context.Background()

	// When/Then: every operation that resolves a stream reports
	// ResourceNotFoundException with HTTP 400, the status the model's default
	// gives a client error with no httpError override.
	_, aerr := s.describeDeliveryStreamTyped(ctx, &describeDeliveryStreamReq{DeliveryStreamName: "nope"})
	assertAWSError(t, aerr, "ResourceNotFoundException", 400)

	_, aerr = s.deleteDeliveryStreamTyped(ctx, &deleteDeliveryStreamReq{DeliveryStreamName: "nope"})
	assertAWSError(t, aerr, "ResourceNotFoundException", 400)

	_, aerr = s.putRecordTyped(ctx, &putRecordReq{DeliveryStreamName: "nope", Record: recordOf(4)})
	assertAWSError(t, aerr, "ResourceNotFoundException", 400)

	_, aerr = s.putRecordBatchTyped(ctx, &putRecordBatchReq{
		DeliveryStreamName: "nope", Records: []firehoseRecord{*recordOf(4)},
	})
	assertAWSError(t, aerr, "ResourceNotFoundException", 400)

	_, aerr = s.listTagsForDeliveryStreamTyped(ctx, &listTagsForDeliveryStreamReq{DeliveryStreamName: "nope"})
	assertAWSError(t, aerr, "ResourceNotFoundException", 400)
}
