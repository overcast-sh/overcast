// Package kinesis_test contains integration tests for the Kinesis emulator.
//
// Run: go test ./tests/integration/kinesis/...
package kinesis_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // reproduces AWS's partition-key hashing, not used for security
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// kinesisCall sends a Kinesis JSON-RPC request via X-Amz-Target header.
func kinesisCall(t *testing.T, srv *helpers.TestServer, op string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Kinesis_20131202."+op)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func kinesisCBORCall(t *testing.T, srv *helpers.TestServer, op string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("marshal CBOR %s body: %v", op, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/Kinesis/operation/"+op, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build CBOR %s request: %v", op, err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do CBOR %s request: %v", op, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func decodeCBOR(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := cbor.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode CBOR response: %v", err)
	}
}

func TestRPCv2CBOR_ListStreams(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := kinesisCBORCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "cbor-list",
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCBORCall(t, srv, "ListStreams", map[string]any{})
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")

	var out struct {
		StreamNames []string `cbor:"StreamNames"`
	}
	decodeCBOR(t, resp, &out)
	if len(out.StreamNames) != 1 || out.StreamNames[0] != "cbor-list" {
		t.Fatalf("StreamNames = %#v, want [cbor-list]", out.StreamNames)
	}
}

func TestRPCv2CBOR_RecordRoundTrip(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := kinesisCBORCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "cbor-records",
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCBORCall(t, srv, "ListShards", map[string]any{
		"StreamName": "cbor-records",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var shardsOut struct {
		Shards []struct {
			ShardId string `cbor:"ShardId"`
		} `cbor:"Shards"`
	}
	decodeCBOR(t, resp, &shardsOut)
	if len(shardsOut.Shards) != 1 {
		t.Fatalf("expected one shard, got %d", len(shardsOut.Shards))
	}

	resp = kinesisCBORCall(t, srv, "GetShardIterator", map[string]any{
		"StreamName":        "cbor-records",
		"ShardId":           shardsOut.Shards[0].ShardId,
		"ShardIteratorType": "TRIM_HORIZON",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var iterOut struct {
		ShardIterator string `cbor:"ShardIterator"`
	}
	decodeCBOR(t, resp, &iterOut)
	if iterOut.ShardIterator == "" {
		t.Fatal("expected ShardIterator")
	}

	resp = kinesisCBORCall(t, srv, "PutRecord", map[string]any{
		"StreamName":   "cbor-records",
		"Data":         []byte("hello"),
		"PartitionKey": "pk",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCBORCall(t, srv, "GetRecords", map[string]any{
		"ShardIterator": iterOut.ShardIterator,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var recordsOut struct {
		Records []struct {
			Data         []byte `cbor:"Data"`
			PartitionKey string `cbor:"PartitionKey"`
		} `cbor:"Records"`
	}
	decodeCBOR(t, resp, &recordsOut)
	if len(recordsOut.Records) != 1 {
		t.Fatalf("expected one record, got %d", len(recordsOut.Records))
	}
	if string(recordsOut.Records[0].Data) != "hello" {
		t.Fatalf("record Data = %q, want hello", string(recordsOut.Records[0].Data))
	}
	if recordsOut.Records[0].PartitionKey != "pk" {
		t.Fatalf("record PartitionKey = %q, want pk", recordsOut.Records[0].PartitionKey)
	}
}

// ---- Shard routing by MD5 hash key range (#1988) --------------------------

// shardsWithHashKeyRanges lists a stream's open shards with their
// HashKeyRange, the same shape TestSplitShard already decodes.
func shardsWithHashKeyRanges(t *testing.T, srv *helpers.TestServer, streamName string) []struct {
	ShardId      string `json:"ShardId"`
	HashKeyRange struct {
		StartingHashKey string `json:"StartingHashKey"`
		EndingHashKey   string `json:"EndingHashKey"`
	} `json:"HashKeyRange"`
} {
	t.Helper()
	resp := kinesisCall(t, srv, "ListShards", map[string]any{"StreamName": streamName})
	var out struct {
		Shards []struct {
			ShardId      string `json:"ShardId"`
			HashKeyRange struct {
				StartingHashKey string `json:"StartingHashKey"`
				EndingHashKey   string `json:"EndingHashKey"`
			} `json:"HashKeyRange"`
		} `json:"Shards"`
	}
	decodeJSON(t, resp, &out)
	return out.Shards
}

// md5HashKey reproduces AWS's PutRecord/PutRecords partition key hashing:
// "An MD5 hash function is used to map partition keys to 128-bit integer
// values" (API reference).
func md5HashKey(partitionKey string) *big.Int {
	sum := md5.Sum([]byte(partitionKey))
	return new(big.Int).SetBytes(sum[:])
}

// shardContaining returns the ShardId of the shard whose HashKeyRange
// contains hashKey, failing the test if none does.
func shardContaining(t *testing.T, shards []struct {
	ShardId      string `json:"ShardId"`
	HashKeyRange struct {
		StartingHashKey string `json:"StartingHashKey"`
		EndingHashKey   string `json:"EndingHashKey"`
	} `json:"HashKeyRange"`
}, hashKey *big.Int) string {
	t.Helper()
	for _, s := range shards {
		start, ok1 := new(big.Int).SetString(s.HashKeyRange.StartingHashKey, 10)
		end, ok2 := new(big.Int).SetString(s.HashKeyRange.EndingHashKey, 10)
		if !ok1 || !ok2 {
			t.Fatalf("shard %s has unparseable HashKeyRange %+v", s.ShardId, s.HashKeyRange)
		}
		if hashKey.Cmp(start) >= 0 && hashKey.Cmp(end) <= 0 {
			return s.ShardId
		}
	}
	t.Fatalf("no shard's HashKeyRange contains hash key %s among %+v", hashKey, shards)
	return ""
}

// TestPutRecord_routesByMD5HashKeyRange asserts a record lands in the shard
// whose reported HashKeyRange actually contains the MD5 hash of its
// partition key — not a byte-sum-modulo-shard-count placement, which
// disagrees with the ranges Overcast itself reports via ListShards/
// DescribeStream.
func TestPutRecord_routesByMD5HashKeyRange(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const streamName = "md5-routing"
	const partitionKey = "route-by-md5-hash"

	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": streamName,
		"ShardCount": 4,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	shards := shardsWithHashKeyRanges(t, srv, streamName)
	if len(shards) != 4 {
		t.Fatalf("expected 4 open shards, got %d", len(shards))
	}
	wantShardID := shardContaining(t, shards, md5HashKey(partitionKey))

	resp = kinesisCall(t, srv, "PutRecord", map[string]any{
		"StreamName":   streamName,
		"Data":         []byte("payload"),
		"PartitionKey": partitionKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		ShardId string `json:"ShardId"`
	}
	decodeJSON(t, resp, &out)

	if out.ShardId != wantShardID {
		t.Fatalf("PutRecord placed partition key %q on %s, want %s (the shard whose HashKeyRange contains its MD5 hash)", partitionKey, out.ShardId, wantShardID)
	}
}

// TestPutRecords_routesByMD5HashKeyRange is PutRecords' counterpart to
// TestPutRecord_routesByMD5HashKeyRange: batched records must use the same
// routing as single-record PutRecord.
func TestPutRecords_routesByMD5HashKeyRange(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const streamName = "md5-routing-batch"
	partitionKeys := []string{"batch-key-a", "batch-key-b", "batch-key-c"}

	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": streamName,
		"ShardCount": 4,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	shards := shardsWithHashKeyRanges(t, srv, streamName)
	want := make([]string, len(partitionKeys))
	records := make([]map[string]any, len(partitionKeys))
	for i, pk := range partitionKeys {
		want[i] = shardContaining(t, shards, md5HashKey(pk))
		records[i] = map[string]any{"Data": []byte("payload"), "PartitionKey": pk}
	}

	resp = kinesisCall(t, srv, "PutRecords", map[string]any{
		"StreamName": streamName,
		"Records":    records,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Records []struct {
			ShardId string `json:"ShardId"`
		} `json:"Records"`
	}
	decodeJSON(t, resp, &out)
	if len(out.Records) != len(want) {
		t.Fatalf("expected %d records in response, got %d", len(want), len(out.Records))
	}
	for i, r := range out.Records {
		if r.ShardId != want[i] {
			t.Fatalf("record[%d] (partition key %q) placed on %s, want %s", i, partitionKeys[i], r.ShardId, want[i])
		}
	}
}

// TestPutRecord_explicitHashKeyOverridesPartitionKey asserts ExplicitHashKey
// wins over the partition key's own MD5 hash when both are given.
func TestPutRecord_explicitHashKeyOverridesPartitionKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const streamName = "explicit-hash-key"
	// A partition key whose own MD5 hash we deliberately do not use.
	const partitionKey = "irrelevant-partition-key"

	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": streamName,
		"ShardCount": 4,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	shards := shardsWithHashKeyRanges(t, srv, streamName)
	naturalShardID := shardContaining(t, shards, md5HashKey(partitionKey))

	// Pick a shard other than the one the partition key would naturally hash
	// to, and use its StartingHashKey as the ExplicitHashKey.
	var targetShard string
	var explicitHashKey string
	for _, s := range shards {
		if s.ShardId != naturalShardID {
			targetShard = s.ShardId
			explicitHashKey = s.HashKeyRange.StartingHashKey
			break
		}
	}
	if targetShard == "" {
		t.Fatal("expected at least one shard different from the partition key's natural shard")
	}

	resp = kinesisCall(t, srv, "PutRecord", map[string]any{
		"StreamName":      streamName,
		"Data":            []byte("payload"),
		"PartitionKey":    partitionKey,
		"ExplicitHashKey": explicitHashKey,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		ShardId string `json:"ShardId"`
	}
	decodeJSON(t, resp, &out)

	if out.ShardId != targetShard {
		t.Fatalf("PutRecord with ExplicitHashKey=%s placed on %s, want %s", explicitHashKey, out.ShardId, targetShard)
	}
}

// ---- PutRecord sequence-number collisions (storage-access-plan.md A1) -----

// TestPutRecord_noSeqNoCollisionAfterDeletion reproduces the A1 hazard: the
// old nextSeqNo derived the next sequence number from len(existing records)
// in the shard. Kinesis has no public per-record delete API, so a record
// can only disappear via retention trim or stream-recreation residue — we
// simulate that here by deleting the stored record directly, the same way
// other services' tests inject storage-layer conditions the wire API can't
// reach (see tests/integration/sqs/sqs_test.go's direct srv.Store.Set
// calls). Before A1: deleting the shard's only record drops its length back
// to 0, so the next PutRecord recomputes sequence number 0 again — the
// exact same key as the deleted record — silently colliding with (and
// masking the loss of) whatever key formatting assumed was unique.
func TestPutRecord_noSeqNoCollisionAfterDeletion(t *testing.T) {
	// Given: a stream with one shard and a single record in it
	srv := helpers.NewTestServer(t)
	const streamName = "seq-collision"

	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": streamName,
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCall(t, srv, "PutRecord", map[string]any{
		"StreamName":   streamName,
		"Data":         []byte("first"),
		"PartitionKey": "pk",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		ShardId        string `json:"ShardId"`
		SequenceNumber string `json:"SequenceNumber"`
	}
	decodeJSON(t, resp, &first)
	if first.SequenceNumber == "" {
		t.Fatal("expected a non-empty SequenceNumber")
	}

	// When: the record is removed directly from the store (simulating
	// retention trim / stream-recreation residue), and a second record is
	// put afterwards.
	ctx := context.Background()
	recordKey := "us-east-1/" + streamName + "/" + first.ShardId + "/" + first.SequenceNumber
	if err := srv.Store.Delete(ctx, "kinesis:records", recordKey); err != nil {
		t.Fatalf("delete record directly: %v", err)
	}

	resp = kinesisCall(t, srv, "PutRecord", map[string]any{
		"StreamName":   streamName,
		"Data":         []byte("second"),
		"PartitionKey": "pk",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var second struct {
		ShardId        string `json:"ShardId"`
		SequenceNumber string `json:"SequenceNumber"`
	}
	decodeJSON(t, resp, &second)

	// Then: the second record must get a strictly greater sequence number —
	// never a repeat of the deleted one.
	if second.SequenceNumber == first.SequenceNumber {
		t.Fatalf("sequence number collision: both PutRecord calls got %q", first.SequenceNumber)
	}
	if second.SequenceNumber <= first.SequenceNumber {
		t.Fatalf("sequence numbers must strictly increase: first=%q second=%q", first.SequenceNumber, second.SequenceNumber)
	}
}

// TestPutRecords_noSeqNoCollisionAfterDeletion is PutRecords' counterpart:
// a whole batch must still allocate strictly-increasing sequence numbers
// for a shard after one of its earlier records was removed.
func TestPutRecords_noSeqNoCollisionAfterDeletion(t *testing.T) {
	// Given: a stream with one shard and one record already in it
	srv := helpers.NewTestServer(t)
	const streamName = "seq-collision-batch"

	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": streamName,
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCall(t, srv, "PutRecord", map[string]any{
		"StreamName":   streamName,
		"Data":         []byte("first"),
		"PartitionKey": "pk",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		ShardId        string `json:"ShardId"`
		SequenceNumber string `json:"SequenceNumber"`
	}
	decodeJSON(t, resp, &first)

	// When: that record is deleted directly, then a 3-record PutRecords
	// batch is submitted to the same shard.
	ctx := context.Background()
	recordKey := "us-east-1/" + streamName + "/" + first.ShardId + "/" + first.SequenceNumber
	if err := srv.Store.Delete(ctx, "kinesis:records", recordKey); err != nil {
		t.Fatalf("delete record directly: %v", err)
	}

	resp = kinesisCall(t, srv, "PutRecords", map[string]any{
		"StreamName": streamName,
		"Records": []map[string]any{
			{"Data": []byte("a"), "PartitionKey": "pk"},
			{"Data": []byte("b"), "PartitionKey": "pk"},
			{"Data": []byte("c"), "PartitionKey": "pk"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var batch struct {
		Records []struct {
			SequenceNumber string `json:"SequenceNumber"`
		} `json:"Records"`
	}
	decodeJSON(t, resp, &batch)

	// Then: none of the batch's sequence numbers repeats the deleted
	// record's, and the batch itself is strictly increasing (a contiguous
	// block allocation, not one collision-prone read-modify-write per
	// record).
	if len(batch.Records) != 3 {
		t.Fatalf("expected 3 records in response, got %d", len(batch.Records))
	}
	prev := first.SequenceNumber
	for i, r := range batch.Records {
		if r.SequenceNumber <= prev {
			t.Fatalf("record[%d] SequenceNumber=%q must be strictly greater than previous=%q", i, r.SequenceNumber, prev)
		}
		prev = r.SequenceNumber
	}
}

// ---- GetRecords iterator resume (storage-access-plan.md A2) ---------------

// TestGetRecords_iteratorResumeNoDuplicatesOrGaps walks a shard's full
// record set through repeated small-Limit GetRecords calls, following
// NextShardIterator each time — mirroring internal/state's ScanPage
// no-duplicates/no-gaps suites (see assertScanPagePaginatesFullRange in
// internal/state/memory_test.go), but over the Kinesis wire API.
func TestGetRecords_iteratorResumeNoDuplicatesOrGaps(t *testing.T) {
	// Given: a stream with one shard and many records
	srv := helpers.NewTestServer(t)
	const streamName = "iter-resume"
	const total = 37
	const pageLimit = 5

	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": streamName,
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	for i := 0; i < total; i++ {
		resp := kinesisCall(t, srv, "PutRecord", map[string]any{
			"StreamName":   streamName,
			"Data":         []byte(fmt.Sprintf("rec-%03d", i)),
			"PartitionKey": "pk",
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}

	resp = kinesisCall(t, srv, "ListShards", map[string]any{"StreamName": streamName})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var shardsOut struct {
		Shards []struct {
			ShardId string `json:"ShardId"`
		} `json:"Shards"`
	}
	decodeJSON(t, resp, &shardsOut)
	if len(shardsOut.Shards) != 1 {
		t.Fatalf("expected one shard, got %d", len(shardsOut.Shards))
	}

	resp = kinesisCall(t, srv, "GetShardIterator", map[string]any{
		"StreamName":        streamName,
		"ShardId":           shardsOut.Shards[0].ShardId,
		"ShardIteratorType": "TRIM_HORIZON",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var iterOut struct {
		ShardIterator string `json:"ShardIterator"`
	}
	decodeJSON(t, resp, &iterOut)

	// When: we page through GetRecords with a Limit well below the total
	// record count, following NextShardIterator each time.
	var got []string
	iter := iterOut.ShardIterator
	for pages := 0; len(got) < total; pages++ {
		if pages > total+5 {
			t.Fatalf("GetRecords did not converge after %d pages (want %d records, got %d): %v", pages, total, len(got), got)
		}
		resp := kinesisCall(t, srv, "GetRecords", map[string]any{
			"ShardIterator": iter,
			"Limit":         pageLimit,
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			Records []struct {
				Data []byte `json:"Data"`
			} `json:"Records"`
			NextShardIterator string `json:"NextShardIterator"`
		}
		decodeJSON(t, resp, &out)
		if len(out.Records) == 0 && out.NextShardIterator == iter {
			t.Fatalf("iterator stalled (no records, no progress) after %d of %d records", len(got), total)
		}
		for _, r := range out.Records {
			got = append(got, string(r.Data))
		}
		iter = out.NextShardIterator
	}

	// Then: every record was returned exactly once, in write order — no
	// skips, no duplicates.
	if len(got) != total {
		t.Fatalf("collected %d records, want %d", len(got), total)
	}
	for i, d := range got {
		want := fmt.Sprintf("rec-%03d", i)
		if d != want {
			t.Fatalf("record[%d] = %q, want %q (skip, duplicate, or reorder)", i, d, want)
		}
	}
}

// ---- SplitShard -------------------------------------------------------------

// TestSplitShard pins the JSON1.1 wire path's behavior for SplitShard,
// which now delegates to splitShardTyped (typed_logic.go) instead of
// duplicating shard-splitting logic in handler.go. There was no existing
// integration coverage for SplitShard at all before this test.
func TestSplitShard(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Given: a stream with a single shard
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "split-test",
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCall(t, srv, "ListShards", map[string]any{
		"StreamName": "split-test",
	})
	var before struct {
		Shards []struct {
			ShardId      string `json:"ShardId"`
			HashKeyRange struct {
				StartingHashKey string `json:"StartingHashKey"`
				EndingHashKey   string `json:"EndingHashKey"`
			} `json:"HashKeyRange"`
		} `json:"Shards"`
	}
	decodeJSON(t, resp, &before)
	if len(before.Shards) != 1 {
		t.Fatalf("expected 1 open shard before split, got %d", len(before.Shards))
	}
	parentShardID := before.Shards[0].ShardId
	startHash := before.Shards[0].HashKeyRange.StartingHashKey
	endHash := before.Shards[0].HashKeyRange.EndingHashKey

	// Split roughly at the midpoint of the shard's hash key range: the
	// exact math isn't the point of this test (that's covered by the
	// shared implementation's own logic in typed_logic.go/handler.go
	// history), the wire round trip is.
	newStartingHashKey := "1"

	// When: SplitShard is called via the JSON1.1 wire (X-Amz-Target), the
	// same path real AWS SDKs use by default.
	resp = kinesisCall(t, srv, "SplitShard", map[string]any{
		"StreamName":         "split-test",
		"ShardToSplit":       parentShardID,
		"NewStartingHashKey": newStartingHashKey,
	})

	// Then: it succeeds with an empty body (matching this handler's
	// pre-existing void-operation convention — see SplitShard's doc
	// comment in handler.go).
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if body != "" {
		t.Fatalf("expected empty body for SplitShard success, got %q", body)
	}

	// And: ListShards now shows two open child shards covering the
	// original hash key range, with the parent no longer open.
	resp = kinesisCall(t, srv, "ListShards", map[string]any{
		"StreamName": "split-test",
	})
	var after struct {
		Shards []struct {
			ShardId      string `json:"ShardId"`
			HashKeyRange struct {
				StartingHashKey string `json:"StartingHashKey"`
				EndingHashKey   string `json:"EndingHashKey"`
			} `json:"HashKeyRange"`
		} `json:"Shards"`
	}
	decodeJSON(t, resp, &after)
	if len(after.Shards) != 2 {
		t.Fatalf("expected 2 open shards after split, got %d", len(after.Shards))
	}
	for _, s := range after.Shards {
		if s.ShardId == parentShardID {
			t.Fatalf("parent shard %s is still open after split", parentShardID)
		}
	}
	gotStart := after.Shards[0].HashKeyRange.StartingHashKey
	gotEnd := after.Shards[1].HashKeyRange.EndingHashKey
	if gotStart != startHash {
		t.Fatalf("first child StartingHashKey = %q, want parent's %q", gotStart, startHash)
	}
	if gotEnd != endHash {
		t.Fatalf("second child EndingHashKey = %q, want parent's %q", gotEnd, endHash)
	}

	resp = kinesisCall(t, srv, "DescribeStreamSummary", map[string]any{
		"StreamName": "split-test",
	})
	var summary struct {
		StreamDescriptionSummary struct {
			OpenShardCount int `json:"OpenShardCount"`
		} `json:"StreamDescriptionSummary"`
	}
	decodeJSON(t, resp, &summary)
	if summary.StreamDescriptionSummary.OpenShardCount != 2 {
		t.Fatalf("expected OpenShardCount=2, got %d", summary.StreamDescriptionSummary.OpenShardCount)
	}
}

// ---- MergeShards -----------------------------------------------------------

func TestMergeShards(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Given: a stream with 2 shards
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "merge-test",
		"ShardCount": 2,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Find two open shards via ListShards
	resp = kinesisCall(t, srv, "ListShards", map[string]any{
		"StreamName": "merge-test",
	})
	var listResp struct {
		Shards []struct {
			ShardId      string `json:"ShardId"`
			HashKeyRange struct {
				StartingHashKey string `json:"StartingHashKey"`
				EndingHashKey   string `json:"EndingHashKey"`
			} `json:"HashKeyRange"`
			SequenceNumberRange struct {
				EndingSequenceNumber string `json:"EndingSequenceNumber"`
			} `json:"SequenceNumberRange"`
		} `json:"Shards"`
	}
	decodeJSON(t, resp, &listResp)

	// Collect open shards
	var openShardIDs []string
	for _, s := range listResp.Shards {
		if s.SequenceNumberRange.EndingSequenceNumber == "" {
			openShardIDs = append(openShardIDs, s.ShardId)
		}
	}
	if len(openShardIDs) < 2 {
		t.Fatalf("expected at least 2 open shards, got %d", len(openShardIDs))
	}

	// When: merge the two shards
	resp = kinesisCall(t, srv, "MergeShards", map[string]any{
		"StreamName":           "merge-test",
		"ShardToMerge":         openShardIDs[0],
		"AdjacentShardToMerge": openShardIDs[1],
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: ListShards should show 1 open shard (the merged one)
	resp = kinesisCall(t, srv, "ListShards", map[string]any{
		"StreamName": "merge-test",
	})
	var afterResp struct {
		Shards []struct {
			ShardId string `json:"ShardId"`
		} `json:"Shards"`
	}
	decodeJSON(t, resp, &afterResp)
	if len(afterResp.Shards) != 1 {
		t.Fatalf("expected 1 open shard after merge, got %d", len(afterResp.Shards))
	}

	// Also verify via DescribeStreamSummary that shard count is 1
	resp = kinesisCall(t, srv, "DescribeStreamSummary", map[string]any{
		"StreamName": "merge-test",
	})
	var summResp struct {
		StreamDescriptionSummary struct {
			OpenShardCount int `json:"OpenShardCount"`
		} `json:"StreamDescriptionSummary"`
	}
	decodeJSON(t, resp, &summResp)
	if summResp.StreamDescriptionSummary.OpenShardCount != 1 {
		t.Fatalf("expected OpenShardCount=1, got %d", summResp.StreamDescriptionSummary.OpenShardCount)
	}
}

// ---- Tags ------------------------------------------------------------------

// listTagsBody calls ListTagsForStream over the JSON1.1 wire and returns the
// raw response body, so callers can compare responses byte-for-byte.
func listTagsBody(t *testing.T, srv *helpers.TestServer, stream string) string {
	t.Helper()
	resp := kinesisCall(t, srv, "ListTagsForStream", map[string]any{"StreamName": stream})
	helpers.AssertStatus(t, resp, http.StatusOK)
	return helpers.ReadBody(t, resp)
}

func TestListTagsForStream_multipleTagsJSON(t *testing.T) {
	// Given: a stream with three tags, added in non-alphabetical key order
	srv := helpers.NewTestServer(t)
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "tag-order",
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = kinesisCall(t, srv, "AddTagsToStream", map[string]any{
		"StreamName": "tag-order",
		"Tags":       map[string]string{"team": "data", "env": "test", "owner": "platform"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: we list the tags
	first := listTagsBody(t, srv, "tag-order")

	// Then: the Tags array is sorted by Key
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if err := json.Unmarshal([]byte(first), &out); err != nil {
		t.Fatalf("decode ListTagsForStream response: %v", err)
	}
	if len(out.Tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(out.Tags))
	}
	for i, want := range []string{"env", "owner", "team"} {
		if out.Tags[i].Key != want {
			t.Fatalf("Tags[%d].Key = %q, want %q (sorted by key)", i, out.Tags[i].Key, want)
		}
	}

	// Then: repeated calls return byte-identical responses
	for i := 0; i < 24; i++ {
		if body := listTagsBody(t, srv, "tag-order"); body != first {
			t.Fatalf("call %d returned different bytes\n got:   %s\n first: %s", i+2, body, first)
		}
	}
}

func TestListTagsForStream_multipleTagsCBOR(t *testing.T) {
	// Given: a stream with three tags, added in non-alphabetical key order
	srv := helpers.NewTestServer(t)
	resp := kinesisCBORCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "cbor-tag-order",
		"ShardCount": 1,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = kinesisCBORCall(t, srv, "AddTagsToStream", map[string]any{
		"StreamName": "cbor-tag-order",
		"Tags":       map[string]string{"team": "data", "env": "test", "owner": "platform"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: we list the tags repeatedly over the CBOR typed path
	// Then: every response returns the Tags array sorted by Key
	for i := 0; i < 25; i++ {
		resp := kinesisCBORCall(t, srv, "ListTagsForStream", map[string]any{
			"StreamName": "cbor-tag-order",
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			Tags []struct {
				Key   string `cbor:"Key"`
				Value string `cbor:"Value"`
			} `cbor:"Tags"`
		}
		decodeCBOR(t, resp, &out)
		if len(out.Tags) != 3 {
			t.Fatalf("call %d: expected 3 tags, got %d", i+1, len(out.Tags))
		}
		for j, want := range []string{"env", "owner", "team"} {
			if out.Tags[j].Key != want {
				t.Fatalf("call %d: Tags[%d].Key = %q, want %q (sorted by key)", i+1, j, out.Tags[j].Key, want)
			}
		}
	}
}

// ---- ARN-based tagging -------------------------------------------------------
//
// Kinesis grew the generic TagResource / UntagResource / ListTagsForResource
// trio alongside the older stream-name operations. They address the resource by
// ARN, and the AWS CLI's `aws kinesis tag-resource` uses them exclusively, so
// the stream-name spellings alone do not make a stream taggable.

func kinesisStreamARN(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := kinesisCall(t, srv, "DescribeStreamSummary", map[string]any{"StreamName": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StreamDescriptionSummary struct {
			StreamARN string `json:"StreamARN"`
		} `json:"StreamDescriptionSummary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode DescribeStreamSummary: %v", err)
	}
	return out.StreamDescriptionSummary.StreamARN
}

func kinesisListTagsForResource(t *testing.T, srv *helpers.TestServer, arn string) map[string]string {
	t.Helper()
	resp := kinesisCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode ListTagsForResource: %v", err)
	}
	got := make(map[string]string, len(out.Tags))
	for _, tag := range out.Tags {
		got[tag.Key] = tag.Value
	}
	return got
}

func TestTagResource_roundTripsByARN(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{"StreamName": "arn-tagged", "ShardCount": 1})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	arn := kinesisStreamARN(t, srv, "arn-tagged")

	resp = kinesisCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags":        map[string]string{"env": "test", "team": "data"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	if got := kinesisListTagsForResource(t, srv, arn); got["env"] != "test" || got["team"] != "data" {
		t.Fatalf("TagResource did not round-trip: got %v", got)
	}

	// The two spellings address the same tag set, not two of them.
	tagsResp := kinesisCall(t, srv, "ListTagsForStream", map[string]any{"StreamName": "arn-tagged"})
	defer tagsResp.Body.Close()
	helpers.AssertStatus(t, tagsResp, http.StatusOK)
	var viaStream struct {
		Tags []struct {
			Key string `json:"Key"`
		} `json:"Tags"`
	}
	if err := json.NewDecoder(tagsResp.Body).Decode(&viaStream); err != nil {
		t.Fatalf("decode ListTagsForStream: %v", err)
	}
	if len(viaStream.Tags) != 2 {
		t.Fatalf("ListTagsForStream sees %d tags after TagResource, want 2", len(viaStream.Tags))
	}
}

func TestUntagResource_removesByARN(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{"StreamName": "arn-untagged", "ShardCount": 1})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	arn := kinesisStreamARN(t, srv, "arn-untagged")

	resp = kinesisCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": arn,
		"Tags":        map[string]string{"env": "test", "keep": "yes"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCall(t, srv, "UntagResource", map[string]any{
		"ResourceARN": arn,
		"TagKeys":     []string{"env"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	got := kinesisListTagsForResource(t, srv, arn)
	if _, still := got["env"]; still {
		t.Errorf("UntagResource left env in place: %v", got)
	}
	if got["keep"] != "yes" {
		t.Errorf("UntagResource removed an unrelated tag: %v", got)
	}
}

// A well-formed ARN naming a stream that does not exist is a not-found, the
// same answer the stream-name spellings give.
// 400 rather than 404: Kinesis models every exception as a client error with
// no httpError override, and the API Reference Errors sections agree
// ("ResourceNotFoundException ... HTTP Status Code: 400"). See errors_test.go.
func TestTagResource_unknownStreamARN(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := kinesisCall(t, srv, "TagResource", map[string]any{
		"ResourceARN": "arn:aws:kinesis:us-east-1:000000000000:stream/does-not-exist",
		"Tags":        map[string]string{"env": "test"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// An ARN that names no stream at all is a bad argument rather than a
// not-found: there is no resource name in it to look up. Consumers are the
// other taggable Kinesis resource and Overcast does not implement them.
func TestTagResource_malformedResourceARN(t *testing.T) {
	srv := helpers.NewTestServer(t)
	for _, arn := range []string{
		"not-an-arn",
		"arn:aws:kinesis:us-east-1:000000000000:consumer/some-consumer",
	} {
		resp := kinesisCall(t, srv, "TagResource", map[string]any{
			"ResourceARN": arn,
			"Tags":        map[string]string{"env": "test"},
		})
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		resp.Body.Close()
	}
}

func TestCreateStream_appliesTagsAtCreation(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "tag-on-create",
		"ShardCount": 1,
		"Tags":       map[string]string{"env": "prod"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	arn := kinesisStreamARN(t, srv, "tag-on-create")
	if got := kinesisListTagsForResource(t, srv, arn); got["env"] != "prod" {
		t.Errorf("CreateStream tags not applied at creation: got %v", got)
	}
}
