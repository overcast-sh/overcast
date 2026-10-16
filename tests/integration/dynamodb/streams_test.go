package dynamodb_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// streamsCall sends a request to the DynamoDBStreams_20120810 target prefix.
func streamsCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDBStreams_20120810."+operation)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("streamsCall %s: %v", operation, err)
	}
	return resp
}

func streamsCBORCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := cbor.Marshal(body)
	if err != nil {
		t.Fatalf("marshal CBOR %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/DynamoDBStreams/operation/"+operation, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build CBOR %s request: %v", operation, err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("streamsCBORCall %s: %v", operation, err)
	}
	return resp
}

// createStreamTable creates a table with streams enabled.
func createStreamTable(t *testing.T, srv *helpers.TestServer, name, viewType string) string {
	t.Helper()
	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName":            name,
		"AttributeDefinitions": []map[string]any{{"AttributeName": "id", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "id", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
		"StreamSpecification": map[string]any{
			"StreamEnabled":  true,
			"StreamViewType": viewType,
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := helpers.ReadBody(t, resp)
		t.Fatalf("createStreamTable %q: status %d: %s", name, resp.StatusCode, body)
	}
	var result struct {
		TableDescription struct {
			LatestStreamArn string `json:"LatestStreamArn"`
		} `json:"TableDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.TableDescription.LatestStreamArn == "" {
		t.Fatalf("createStreamTable %q: no LatestStreamArn in response", name)
	}
	return result.TableDescription.LatestStreamArn
}

// ---- CreateTable with StreamSpecification ----------------------------------

func TestStream_CreateTable_WithStream(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := ddbCall(t, srv, "CreateTable", map[string]any{
		"TableName":            "events",
		"AttributeDefinitions": []map[string]any{{"AttributeName": "id", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "id", "KeyType": "HASH"}},
		"BillingMode":          "PAY_PER_REQUEST",
		"StreamSpecification": map[string]any{
			"StreamEnabled":  true,
			"StreamViewType": "NEW_AND_OLD_IMAGES",
		},
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		TableDescription struct {
			LatestStreamArn     string `json:"LatestStreamArn"`
			LatestStreamLabel   string `json:"LatestStreamLabel"`
			StreamSpecification struct {
				StreamEnabled  bool   `json:"StreamEnabled"`
				StreamViewType string `json:"StreamViewType"`
			} `json:"StreamSpecification"`
		} `json:"TableDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if result.TableDescription.LatestStreamArn == "" {
		t.Error("expected LatestStreamArn to be set")
	}
	if result.TableDescription.LatestStreamLabel == "" {
		t.Error("expected LatestStreamLabel to be set")
	}
	if !result.TableDescription.StreamSpecification.StreamEnabled {
		t.Error("expected StreamEnabled true")
	}
	if result.TableDescription.StreamSpecification.StreamViewType != "NEW_AND_OLD_IMAGES" {
		t.Errorf("expected StreamViewType NEW_AND_OLD_IMAGES, got %q", result.TableDescription.StreamSpecification.StreamViewType)
	}
}

// ---- UpdateTable: enable / disable streams ---------------------------------

func TestStream_UpdateTable_Enable(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "users")

	resp := ddbCall(t, srv, "UpdateTable", map[string]any{
		"TableName": "users",
		"StreamSpecification": map[string]any{
			"StreamEnabled":  true,
			"StreamViewType": "NEW_IMAGE",
		},
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		TableDescription struct {
			LatestStreamArn     string `json:"LatestStreamArn"`
			StreamSpecification struct {
				StreamEnabled bool `json:"StreamEnabled"`
			} `json:"StreamSpecification"`
		} `json:"TableDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if result.TableDescription.LatestStreamArn == "" {
		t.Error("expected LatestStreamArn after enabling stream")
	}
	if !result.TableDescription.StreamSpecification.StreamEnabled {
		t.Error("expected StreamEnabled true")
	}
}

func TestStream_UpdateTable_Disable(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStreamTable(t, srv, "users", "NEW_IMAGE")

	resp := ddbCall(t, srv, "UpdateTable", map[string]any{
		"TableName": "users",
		"StreamSpecification": map[string]any{
			"StreamEnabled": false,
		},
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		TableDescription struct {
			StreamSpecification struct {
				StreamEnabled bool `json:"StreamEnabled"`
			} `json:"StreamSpecification"`
		} `json:"TableDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if result.TableDescription.StreamSpecification.StreamEnabled {
		t.Error("expected StreamEnabled false after disabling")
	}
}

// ---- ListStreams ------------------------------------------------------------

func TestStream_ListStreams_Empty(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createTable(t, srv, "no-stream")

	resp := streamsCall(t, srv, "ListStreams", map[string]any{})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Streams []any `json:"Streams"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Streams) != 0 {
		t.Errorf("expected 0 streams, got %d", len(result.Streams))
	}
}

func TestStream_ListStreams_ReturnsEnabled(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "orders", "KEYS_ONLY")
	createTable(t, srv, "no-stream") // should not appear

	resp := streamsCall(t, srv, "ListStreams", map[string]any{})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Streams []struct {
			StreamArn   string `json:"StreamArn"`
			StreamLabel string `json:"StreamLabel"`
			TableName   string `json:"TableName"`
		} `json:"Streams"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(result.Streams))
	}
	if result.Streams[0].StreamArn != streamArn {
		t.Errorf("expected StreamArn %q, got %q", streamArn, result.Streams[0].StreamArn)
	}
	if result.Streams[0].TableName != "orders" {
		t.Errorf("expected TableName orders, got %q", result.Streams[0].TableName)
	}
}

func TestStream_ListStreams_FilterByTable(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStreamTable(t, srv, "orders", "KEYS_ONLY")
	createStreamTable(t, srv, "users", "NEW_IMAGE")

	resp := streamsCall(t, srv, "ListStreams", map[string]any{
		"TableName": "orders",
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Streams []struct {
			TableName string `json:"TableName"`
		} `json:"Streams"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(result.Streams))
	}
	if result.Streams[0].TableName != "orders" {
		t.Errorf("expected orders, got %q", result.Streams[0].TableName)
	}
}

func TestRPCv2CBOR_StreamListStreams(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "orders", "KEYS_ONLY")
	createStreamTable(t, srv, "users", "NEW_IMAGE")

	resp := streamsCBORCall(t, srv, "ListStreams", map[string]any{
		"TableName": "orders",
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "application/cbor" {
		t.Fatalf("Content-Type = %q", got)
	}

	var result struct {
		Streams []struct {
			StreamArn string `cbor:"StreamArn"`
			TableName string `cbor:"TableName"`
		} `cbor:"Streams"`
	}
	if err := cbor.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode CBOR ListStreams response: %v", err)
	}
	if len(result.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(result.Streams))
	}
	if result.Streams[0].StreamArn != streamArn {
		t.Errorf("expected StreamArn %q, got %q", streamArn, result.Streams[0].StreamArn)
	}
	if result.Streams[0].TableName != "orders" {
		t.Errorf("expected TableName orders, got %q", result.Streams[0].TableName)
	}
}

// ---- DescribeStream --------------------------------------------------------

func TestStream_DescribeStream(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_AND_OLD_IMAGES")

	resp := streamsCall(t, srv, "DescribeStream", map[string]any{
		"StreamArn": streamArn,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		StreamDescription struct {
			StreamArn      string `json:"StreamArn"`
			StreamStatus   string `json:"StreamStatus"`
			StreamViewType string `json:"StreamViewType"`
			TableName      string `json:"TableName"`
			Shards         []struct {
				ShardId string `json:"ShardId"`
			} `json:"Shards"`
		} `json:"StreamDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if result.StreamDescription.StreamArn != streamArn {
		t.Errorf("expected StreamArn %q, got %q", streamArn, result.StreamDescription.StreamArn)
	}
	if result.StreamDescription.StreamStatus != "ENABLED" {
		t.Errorf("expected StreamStatus ENABLED, got %q", result.StreamDescription.StreamStatus)
	}
	if result.StreamDescription.StreamViewType != "NEW_AND_OLD_IMAGES" {
		t.Errorf("unexpected StreamViewType: %q", result.StreamDescription.StreamViewType)
	}
	if result.StreamDescription.TableName != "items" {
		t.Errorf("expected TableName items, got %q", result.StreamDescription.TableName)
	}
	if len(result.StreamDescription.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(result.StreamDescription.Shards))
	}
	if result.StreamDescription.Shards[0].ShardId == "" {
		t.Error("expected non-empty ShardId")
	}
}

func TestStream_DescribeStream_NotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := streamsCall(t, srv, "DescribeStream", map[string]any{
		"StreamArn": "arn:aws:dynamodb:us-east-1:000000000000:table/no-such/stream/2024",
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ---- GetShardIterator / GetRecords -----------------------------------------

func TestStream_GetShardIterator(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_IMAGE")
	shardId := describeStreamShardId(t, srv, streamArn)

	resp := streamsCall(t, srv, "GetShardIterator", map[string]any{
		"StreamArn":         streamArn,
		"ShardId":           shardId,
		"ShardIteratorType": "TRIM_HORIZON",
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		ShardIterator string `json:"ShardIterator"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if result.ShardIterator == "" {
		t.Error("expected non-empty ShardIterator")
	}
	// Must be valid base64.
	if _, err := base64.StdEncoding.DecodeString(result.ShardIterator); err != nil {
		t.Errorf("ShardIterator is not base64: %v", err)
	}
}

// TestStream_GetShardIterator_atTimestamp pins AT_TIMESTAMP: the iterator
// starts at the first record created at or after the timestamp, an ancient
// timestamp reads from the trim horizon, a missing timestamp is refused, and a
// future one is a ValidationException.
func TestStream_GetShardIterator_atTimestamp(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_IMAGE")
	shardId := describeStreamShardId(t, srv, streamArn)

	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "a"}})
	time.Sleep(20 * time.Millisecond)
	cut := float64(time.Now().UnixMilli()) / 1000.0
	time.Sleep(20 * time.Millisecond)
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "b"}})

	iterAt := func(ts any) *http.Response {
		body := map[string]any{"StreamArn": streamArn, "ShardId": shardId, "ShardIteratorType": "AT_TIMESTAMP"}
		if ts != nil {
			body["Timestamp"] = ts
		}
		return streamsCall(t, srv, "GetShardIterator", body)
	}
	recordCount := func(resp *http.Response) int {
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		var it struct{ ShardIterator string }
		helpers.DecodeJSON(t, resp, &it)
		rr := streamsCall(t, srv, "GetRecords", map[string]any{"ShardIterator": it.ShardIterator})
		defer rr.Body.Close()
		helpers.AssertStatus(t, rr, http.StatusOK)
		var out struct{ Records []map[string]any }
		helpers.DecodeJSON(t, rr, &out)
		return len(out.Records)
	}

	if n := recordCount(iterAt(cut)); n != 1 {
		t.Errorf("timestamp between writes: got %d records, want 1", n)
	}
	if n := recordCount(iterAt(1.0)); n != 2 {
		t.Errorf("ancient timestamp: got %d records, want 2", n)
	}

	resp := iterAt(nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")

	future := iterAt(float64(time.Now().Add(time.Hour).UnixMilli()) / 1000.0)
	defer future.Body.Close()
	helpers.AssertStatus(t, future, http.StatusBadRequest)
	helpers.AssertJSONError(t, future, "ValidationException")
}

func TestStream_GetRecords_AfterPut(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_AND_OLD_IMAGES")
	shardId := describeStreamShardId(t, srv, streamArn)

	// Get TRIM_HORIZON iterator (reads from start).
	iter := getShardIterator(t, srv, streamArn, shardId, "TRIM_HORIZON", "")

	// Put two items.
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "a"}})
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "b"}})

	// Read records.
	resp := streamsCall(t, srv, "GetRecords", map[string]any{
		"ShardIterator": iter,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			EventName string `json:"eventName"`
			Dynamodb  struct {
				Keys     map[string]any `json:"Keys"`
				NewImage map[string]any `json:"NewImage"`
			} `json:"dynamodb"`
		} `json:"Records"`
		NextShardIterator string `json:"NextShardIterator"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(result.Records))
	}
	if result.Records[0].EventName != "INSERT" {
		t.Errorf("expected INSERT, got %q", result.Records[0].EventName)
	}
	// Keys must be present for all view types.
	if result.Records[0].Dynamodb.Keys["id"] == nil {
		t.Error("expected Keys.id to be present")
	}
	// NewImage must be present for NEW_AND_OLD_IMAGES.
	if result.Records[0].Dynamodb.NewImage["id"] == nil {
		t.Error("expected NewImage.id to be present")
	}
	if result.NextShardIterator == "" {
		t.Error("expected NextShardIterator to be non-empty")
	}
}

// TestStream_recreatedTableStartsWithAnEmptyStream pins that a table's stream
// dies with the table. Stream records are keyed by table name, so a table
// deleted and recreated under that name used to inherit the previous table's
// history: DescribeStream reported a fresh stream (new ARN and label, because
// the ARN carries a creation timestamp), but a TRIM_HORIZON read replayed
// changes made to a table that no longer existed.
func TestStream_recreatedTableStartsWithAnEmptyStream(t *testing.T) {
	// Given: a streamed table that recorded a change, then was deleted.
	srv := helpers.NewTestServer(t)
	createStreamTable(t, srv, "recreated-items", "NEW_AND_OLD_IMAGES")
	putItem(t, srv, "recreated-items", map[string]any{"id": map[string]string{"S": "from-old-table"}})

	deleteResp := ddbCall(t, srv, "DeleteTable", map[string]any{"TableName": "recreated-items"})
	defer deleteResp.Body.Close()
	helpers.AssertStatus(t, deleteResp, http.StatusOK)

	// When: a table with the same name is recreated, and its stream is read
	// from the earliest point available.
	streamArn := createStreamTable(t, srv, "recreated-items", "NEW_AND_OLD_IMAGES")
	shardID := describeStreamShardId(t, srv, streamArn)
	iter := getShardIterator(t, srv, streamArn, shardID, "TRIM_HORIZON", "")

	resp := streamsCall(t, srv, "GetRecords", map[string]any{"ShardIterator": iter})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the new stream carries none of the deleted table's changes.
	var result struct {
		Records []struct {
			EventName string `json:"eventName"`
			Dynamodb  struct {
				Keys map[string]any `json:"Keys"`
			} `json:"dynamodb"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Records) != 0 {
		t.Fatalf("expected an empty stream on the recreated table, got %d record(s); first: %s %v",
			len(result.Records), result.Records[0].EventName, result.Records[0].Dynamodb.Keys)
	}

	// And: it still records changes made to the new table.
	putItem(t, srv, "recreated-items", map[string]any{"id": map[string]string{"S": "from-new-table"}})
	resp2 := streamsCall(t, srv, "GetRecords", map[string]any{"ShardIterator": iter})
	defer resp2.Body.Close()
	result.Records = nil
	helpers.DecodeJSON(t, resp2, &result)
	if len(result.Records) != 1 {
		t.Fatalf("expected 1 record from the recreated table, got %d", len(result.Records))
	}
	if keys, _ := result.Records[0].Dynamodb.Keys["id"].(map[string]any); keys["S"] != "from-new-table" {
		t.Errorf("expected the new table's record, got %v", result.Records[0].Dynamodb.Keys)
	}
}

func TestStream_GetRecords_AfterDelete(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_AND_OLD_IMAGES")
	shardId := describeStreamShardId(t, srv, streamArn)

	// Put then delete.
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "x"}})

	iter := getShardIterator(t, srv, streamArn, shardId, "TRIM_HORIZON", "")

	ddbCall(t, srv, "DeleteItem", map[string]any{
		"TableName": "items",
		"Key":       map[string]any{"id": map[string]string{"S": "x"}},
	}).Body.Close()

	resp := streamsCall(t, srv, "GetRecords", map[string]any{
		"ShardIterator": iter,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			EventName string `json:"eventName"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)

	// Expect: INSERT for the put + REMOVE for the delete (iter was TRIM_HORIZON
	// but the PUT happened before the iterator was created — we get both since
	// TRIM_HORIZON reads from the very beginning).
	found := map[string]bool{}
	for _, r := range result.Records {
		found[r.EventName] = true
	}
	if !found["INSERT"] {
		t.Error("expected INSERT record")
	}
	if !found["REMOVE"] {
		t.Error("expected REMOVE record")
	}
}

func TestStream_GetRecords_LATEST_SeesOnlyNewRecords(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_IMAGE")
	shardId := describeStreamShardId(t, srv, streamArn)

	// Write before getting iterator.
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "old"}})

	// Get LATEST iterator — should not see the above put.
	iter := getShardIterator(t, srv, streamArn, shardId, "LATEST", "")

	// Write after getting iterator.
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "new"}})

	resp := streamsCall(t, srv, "GetRecords", map[string]any{
		"ShardIterator": iter,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			Dynamodb struct {
				Keys map[string]any `json:"Keys"`
			} `json:"dynamodb"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Records) != 1 {
		t.Fatalf("LATEST iterator: expected 1 record (new write only), got %d", len(result.Records))
	}
}

func TestStream_GetRecords_KEYS_ONLY(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "KEYS_ONLY")
	shardId := describeStreamShardId(t, srv, streamArn)
	iter := getShardIterator(t, srv, streamArn, shardId, "TRIM_HORIZON", "")

	putItem(t, srv, "items", map[string]any{
		"id":   map[string]string{"S": "k1"},
		"data": map[string]string{"S": "should not appear"},
	})

	resp := streamsCall(t, srv, "GetRecords", map[string]any{
		"ShardIterator": iter,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			Dynamodb struct {
				Keys     map[string]any `json:"Keys"`
				NewImage map[string]any `json:"NewImage"`
			} `json:"dynamodb"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(result.Records))
	}
	if result.Records[0].Dynamodb.Keys["id"] == nil {
		t.Error("expected Keys.id for KEYS_ONLY")
	}
	if len(result.Records[0].Dynamodb.NewImage) != 0 {
		t.Errorf("KEYS_ONLY: NewImage should be empty, got %v", result.Records[0].Dynamodb.NewImage)
	}
}

func TestStream_PutItem_ModifyGeneratesRecord(t *testing.T) {
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "items", "NEW_AND_OLD_IMAGES")
	shardId := describeStreamShardId(t, srv, streamArn)

	// First put (INSERT).
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "m1"}, "v": map[string]string{"S": "v1"}})

	iter := getShardIterator(t, srv, streamArn, shardId, "LATEST", "")

	// Second put to same key (MODIFY).
	putItem(t, srv, "items", map[string]any{"id": map[string]string{"S": "m1"}, "v": map[string]string{"S": "v2"}})

	resp := streamsCall(t, srv, "GetRecords", map[string]any{
		"ShardIterator": iter,
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			EventName string `json:"eventName"`
			Dynamodb  struct {
				OldImage map[string]any `json:"OldImage"`
				NewImage map[string]any `json:"NewImage"`
			} `json:"dynamodb"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Records) != 1 {
		t.Fatalf("expected 1 MODIFY record, got %d", len(result.Records))
	}
	if result.Records[0].EventName != "MODIFY" {
		t.Errorf("expected MODIFY, got %q", result.Records[0].EventName)
	}
	if result.Records[0].Dynamodb.OldImage["v"] == nil {
		t.Error("expected OldImage.v for MODIFY event")
	}
	if result.Records[0].Dynamodb.NewImage["v"] == nil {
		t.Error("expected NewImage.v for MODIFY event")
	}
}

func TestStream_BatchWriteItem_putsGenerateRecords(t *testing.T) {
	// Given: a stream-enabled table and an iterator at the start of the stream.
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "batch-stream-items", "NEW_AND_OLD_IMAGES")
	shardId := describeStreamShardId(t, srv, streamArn)
	iter := getShardIterator(t, srv, streamArn, shardId, "TRIM_HORIZON", "")

	// When: BatchWriteItem writes two items.
	resp := ddbCall(t, srv, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"batch-stream-items": []map[string]any{
				{"PutRequest": map[string]any{"Item": map[string]any{"id": map[string]string{"S": "b1"}}}},
				{"PutRequest": map[string]any{"Item": map[string]any{"id": map[string]string{"S": "b2"}}}},
			},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: both writes are available as stream INSERT records.
	resp = streamsCall(t, srv, "GetRecords", map[string]any{"ShardIterator": iter})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			EventName string `json:"eventName"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Records) != 2 {
		t.Fatalf("expected 2 BatchWriteItem stream records, got %d", len(result.Records))
	}
	for i, record := range result.Records {
		if record.EventName != "INSERT" {
			t.Fatalf("record %d: expected INSERT, got %q", i, record.EventName)
		}
	}
}

func TestStream_TransactWriteItems_putsGenerateRecords(t *testing.T) {
	// Given: a stream-enabled table and an iterator at the start of the stream.
	srv := helpers.NewTestServer(t)
	streamArn := createStreamTable(t, srv, "transact-stream-items", "NEW_AND_OLD_IMAGES")
	shardId := describeStreamShardId(t, srv, streamArn)
	iter := getShardIterator(t, srv, streamArn, shardId, "TRIM_HORIZON", "")

	// When: TransactWriteItems writes two items atomically.
	resp := ddbCall(t, srv, "TransactWriteItems", map[string]any{
		"TransactItems": []map[string]any{
			{"Put": map[string]any{"TableName": "transact-stream-items", "Item": map[string]any{"id": map[string]string{"S": "t1"}}}},
			{"Put": map[string]any{"TableName": "transact-stream-items", "Item": map[string]any{"id": map[string]string{"S": "t2"}}}},
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: both writes are available as stream INSERT records.
	resp = streamsCall(t, srv, "GetRecords", map[string]any{"ShardIterator": iter})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Records []struct {
			EventName string `json:"eventName"`
		} `json:"Records"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Records) != 2 {
		t.Fatalf("expected 2 TransactWriteItems stream records, got %d", len(result.Records))
	}
	for i, record := range result.Records {
		if record.EventName != "INSERT" {
			t.Fatalf("record %d: expected INSERT, got %q", i, record.EventName)
		}
	}
}

// ---- helper functions ------------------------------------------------------

func describeStreamShardId(t *testing.T, srv *helpers.TestServer, streamArn string) string {
	t.Helper()
	resp := streamsCall(t, srv, "DescribeStream", map[string]any{
		"StreamArn": streamArn,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		StreamDescription struct {
			Shards []struct {
				ShardId string `json:"ShardId"`
			} `json:"Shards"`
		} `json:"StreamDescription"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.StreamDescription.Shards) == 0 {
		t.Fatalf("describeStream: no shards returned")
	}
	return result.StreamDescription.Shards[0].ShardId
}

func getShardIterator(t *testing.T, srv *helpers.TestServer, streamArn, shardId, iterType, seqNum string) string {
	t.Helper()
	body := map[string]any{
		"StreamArn":         streamArn,
		"ShardId":           shardId,
		"ShardIteratorType": iterType,
	}
	if seqNum != "" {
		body["SequenceNumber"] = seqNum
	}
	resp := streamsCall(t, srv, "GetShardIterator", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		ShardIterator string `json:"ShardIterator"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ShardIterator == "" {
		t.Fatalf("getShardIterator: empty ShardIterator")
	}
	return result.ShardIterator
}
