// errors_test.go pins the error codes and HTTP statuses Kinesis answers with
// (#155).
//
// Two divergences this file was written to catch:
//
//   - Every exception in the Kinesis model is `@error("client")` with no
//     `@httpError` override, so all of them are HTTP 400 — including
//     ResourceNotFoundException, which Overcast answered with 404. The AWS
//     API Reference says the same on each operation's Errors section
//     ("ResourceNotFoundException ... HTTP Status Code: 400").
//   - A missing required parameter answered `MissingParameter`, a code no
//     Kinesis operation models, so no SDK's error type matches it. Kinesis
//     models InvalidArgumentException on every operation here and this
//     package already reports its other rejected-input cases that way (see
//     kinesisTagCfg in internal/services/kinesis/typed_logic.go).
//
// AWS reference:
// https://docs.aws.amazon.com/kinesis/latest/APIReference/CommonErrors.html
package kinesis_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// assertKinesisError asserts an operation's error code and HTTP status.
func assertKinesisError(t *testing.T, srv *helpers.TestServer, op string, body map[string]any, code string, status int) {
	t.Helper()
	resp := kinesisCall(t, srv, op, body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, status)
	helpers.AssertJSONError(t, resp, code)
}

func TestKinesis_unknownStreamIsResourceNotFoundWith400(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, op := range []string{"DescribeStream", "DescribeStreamSummary", "ListShards", "ListTagsForStream", "DeleteStream"} {
		t.Run(op, func(t *testing.T) {
			assertKinesisError(t, srv, op, map[string]any{"StreamName": "no-such-stream"},
				"ResourceNotFoundException", http.StatusBadRequest)
		})
	}
}

func TestKinesis_missingStreamIdentifierIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, op := range []string{"DescribeStream", "DescribeStreamSummary", "DeleteStream", "ListTagsForStream", "PutRecord", "SplitShard"} {
		t.Run(op, func(t *testing.T) {
			assertKinesisError(t, srv, op, map[string]any{},
				"InvalidArgumentException", http.StatusBadRequest)
		})
	}
}

// TestPutRecord_explicitHashKeyOutOfRangeIsInvalidArgument asserts an
// ExplicitHashKey that falls outside every open shard's HashKeyRange — here,
// a value past the 128-bit hash space (2^128-1 = 39 digits) every shard's
// range is carved from — is rejected with the modeled InvalidArgumentException
// rather than silently accepted (#1988).
func TestPutRecord_explicitHashKeyOutOfRangeIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "explicit-hash-key-errors", 2)

	assertKinesisError(t, srv, "PutRecord", map[string]any{
		"StreamName":      "explicit-hash-key-errors",
		"Data":            []byte("payload"),
		"PartitionKey":    "pk",
		"ExplicitHashKey": "9999999999999999999999999999999999999999",
	}, "InvalidArgumentException", http.StatusBadRequest)
}

func TestSplitShard_unknownShardIsResourceNotFoundWith400(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "split-errors", 1)

	assertKinesisError(t, srv, "SplitShard", map[string]any{
		"StreamName":         "split-errors",
		"ShardToSplit":       "shardId-999999999999",
		"NewStartingHashKey": "10",
	}, "ResourceNotFoundException", http.StatusBadRequest)
}

func TestMergeShards_unknownShardIsResourceNotFoundWith400(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "merge-errors", 2)
	ids := openShardIDs(t, srv, "merge-errors", 2)

	assertKinesisError(t, srv, "MergeShards", map[string]any{
		"StreamName":           "merge-errors",
		"ShardToMerge":         ids[0],
		"AdjacentShardToMerge": "shardId-999999999999",
	}, "ResourceNotFoundException", http.StatusBadRequest)
}

func TestCreateStream_duplicateIsResourceInUse(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "dupe", 1)

	assertKinesisError(t, srv, "CreateStream", map[string]any{"StreamName": "dupe", "ShardCount": 1},
		"ResourceInUseException", http.StatusBadRequest)
}

func TestGetRecords_malformedShardIteratorIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for name, iterator := range map[string]string{
		"not base64":     "!!!not-base64!!!",
		"wrong encoding": "bm90LWFuLWl0ZXJhdG9y", // base64 of "not-an-iterator"
	} {
		t.Run(name, func(t *testing.T) {
			assertKinesisError(t, srv, "GetRecords", map[string]any{"ShardIterator": iterator},
				"InvalidArgumentException", http.StatusBadRequest)
		})
	}
}

func TestGetRecords_missingShardIteratorIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)

	assertKinesisError(t, srv, "GetRecords", map[string]any{},
		"InvalidArgumentException", http.StatusBadRequest)
}
