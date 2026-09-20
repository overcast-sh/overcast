// pagination_test.go covers the four Kinesis operations AWS documents as
// paginated — ListStreams, ListShards, DescribeStream and ListTagsForStream
// (#155). Each one carries a cursor parameter, a page-size parameter and a
// "there is more" flag in its output, and Overcast used to ignore all three
// and answer every call with the whole set in a single page.
//
// Token expiry (nextTokenTTL / ExpiredNextTokenException) is covered by unit
// tests in internal/services/kinesis/pagination_test.go — aging a token
// through this surface means winding the test server's mock clock past 300
// virtual seconds, which costs seconds of wall time per test.
//
// AWS references:
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListStreams.html
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStream.html
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListTagsForStream.html
package kinesis_test

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- fixtures ---------------------------------------------------------------

func createStream(t *testing.T, srv *helpers.TestServer, name string, shardCount int) {
	t.Helper()
	resp := kinesisCall(t, srv, "CreateStream", map[string]any{
		"StreamName": name, "ShardCount": shardCount,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

type listStreamsPage struct {
	StreamNames     []string `json:"StreamNames"`
	HasMoreStreams  bool     `json:"HasMoreStreams"`
	NextToken       string   `json:"NextToken"`
	StreamSummaries []struct {
		StreamName        string `json:"StreamName"`
		StreamARN         string `json:"StreamARN"`
		StreamStatus      string `json:"StreamStatus"`
		StreamModeDetails struct {
			StreamMode string `json:"StreamMode"`
		} `json:"StreamModeDetails"`
		StreamCreationTimestamp int64 `json:"StreamCreationTimestamp"`
	} `json:"StreamSummaries"`
}

func listStreams(t *testing.T, srv *helpers.TestServer, body map[string]any) listStreamsPage {
	t.Helper()
	resp := kinesisCall(t, srv, "ListStreams", body)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listStreamsPage
	decodeJSON(t, resp, &out)
	return out
}

type listShardsPage struct {
	Shards []struct {
		ShardId string `json:"ShardId"`
	} `json:"Shards"`
	NextToken string `json:"NextToken"`
}

func listShards(t *testing.T, srv *helpers.TestServer, body map[string]any) listShardsPage {
	t.Helper()
	resp := kinesisCall(t, srv, "ListShards", body)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listShardsPage
	decodeJSON(t, resp, &out)
	return out
}

// openShardIDs returns the stream's open shard IDs in listing order, asking
// for a page big enough to hold them all.
func openShardIDs(t *testing.T, srv *helpers.TestServer, stream string, want int) []string {
	t.Helper()
	page := listShards(t, srv, map[string]any{"StreamName": stream, "MaxResults": 1000})
	ids := make([]string, 0, len(page.Shards))
	for _, s := range page.Shards {
		ids = append(ids, s.ShardId)
	}
	if len(ids) != want {
		t.Fatalf("stream %s has %d open shards (%v), want %d", stream, len(ids), ids, want)
	}
	return ids
}

// ---- ListStreams -------------------------------------------------------------

// A page walk must deliver every stream exactly once, in order, and the last
// page must clear HasMoreStreams and carry no NextToken.
func TestListStreams_pageWalkDeliversEveryStreamExactlyOnce(t *testing.T) {
	// Given: five streams and a page size of two
	srv := helpers.NewTestServer(t)
	want := []string{"page-a", "page-b", "page-c", "page-d", "page-e"}
	for _, name := range want {
		createStream(t, srv, name, 1)
	}

	// When: we walk the pages
	var got []string
	body := map[string]any{"Limit": 2}
	for i := 0; ; i++ {
		if i > 10 {
			t.Fatal("ListStreams page walk did not terminate")
		}
		page := listStreams(t, srv, body)
		got = append(got, page.StreamNames...)
		if !page.HasMoreStreams {
			if page.NextToken != "" {
				t.Errorf("terminal page carries NextToken %q, want empty", page.NextToken)
			}
			if len(page.StreamNames) > 2 {
				t.Errorf("terminal page has %d streams, want at most the Limit of 2", len(page.StreamNames))
			}
			break
		}
		if len(page.StreamNames) != 2 {
			t.Fatalf("non-terminal page has %d streams, want the Limit of 2", len(page.StreamNames))
		}
		if page.NextToken == "" {
			t.Fatal("HasMoreStreams is true but NextToken is empty")
		}
		body = map[string]any{"Limit": 2, "NextToken": page.NextToken}
	}

	// Then: every stream arrived exactly once, in name order
	if len(got) != len(want) {
		t.Fatalf("page walk returned %d streams (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page walk returned %v, want %v", got, want)
		}
	}
}

// ExclusiveStartStreamName is AWS's documented continuation parameter: the
// listing resumes after that name.
func TestListStreams_exclusiveStartStreamNameResumesAfterThatName(t *testing.T) {
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"excl-a", "excl-b", "excl-c"} {
		createStream(t, srv, name, 1)
	}

	page := listStreams(t, srv, map[string]any{"ExclusiveStartStreamName": "excl-a"})

	if len(page.StreamNames) != 2 || page.StreamNames[0] != "excl-b" || page.StreamNames[1] != "excl-c" {
		t.Fatalf("StreamNames = %v, want [excl-b excl-c]", page.StreamNames)
	}
}

// StreamSummaries is a modeled member of ListStreamsOutput and carries the
// ARN, status, mode and creation time of each listed stream.
func TestListStreams_returnsStreamSummariesAlongsideNames(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "summary-stream", 1)

	page := listStreams(t, srv, map[string]any{})

	if len(page.StreamSummaries) != 1 {
		t.Fatalf("StreamSummaries = %#v, want one entry", page.StreamSummaries)
	}
	got := page.StreamSummaries[0]
	if got.StreamName != "summary-stream" {
		t.Errorf("StreamName = %q, want summary-stream", got.StreamName)
	}
	if got.StreamARN == "" {
		t.Error("StreamARN is empty")
	}
	if got.StreamStatus != "ACTIVE" {
		t.Errorf("StreamStatus = %q, want ACTIVE", got.StreamStatus)
	}
	if got.StreamModeDetails.StreamMode != "PROVISIONED" {
		t.Errorf("StreamMode = %q, want PROVISIONED", got.StreamModeDetails.StreamMode)
	}
	if got.StreamCreationTimestamp == 0 {
		t.Error("StreamCreationTimestamp is zero")
	}
}

func TestListStreams_malformedNextTokenIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "bad-token", 1)

	resp := kinesisCall(t, srv, "ListStreams", map[string]any{"NextToken": "not-a-real-token"})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

// The model constrains Limit to 1..10000; zero and negative values are out of
// range rather than "unset".
func TestListStreams_outOfRangeLimitIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "limit-range", 1)

	for _, limit := range []int{0, -1, 10001} {
		resp := kinesisCall(t, srv, "ListStreams", map[string]any{"Limit": limit})
		helpers.AssertStatus(t, resp, http.StatusBadRequest)
		helpers.AssertJSONError(t, resp, "InvalidArgumentException")
		resp.Body.Close()
	}
}

// ---- ListShards --------------------------------------------------------------

func TestListShards_pageWalkDeliversEveryShardExactlyOnce(t *testing.T) {
	// Given: a stream with five open shards
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "shard-pages", 5)
	want := openShardIDs(t, srv, "shard-pages", 5)

	// When: we walk the pages two at a time
	var got []string
	pages := 0
	body := map[string]any{"StreamName": "shard-pages", "MaxResults": 2}
	for {
		if pages > 10 {
			t.Fatal("ListShards page walk did not terminate")
		}
		page := listShards(t, srv, body)
		pages++
		for _, s := range page.Shards {
			got = append(got, s.ShardId)
		}
		if page.NextToken == "" {
			if len(page.Shards) > 2 {
				t.Fatalf("terminal page has %d shards, want at most the MaxResults of 2", len(page.Shards))
			}
			break
		}
		if len(page.Shards) != 2 {
			t.Fatalf("non-terminal page has %d shards, want the MaxResults of 2", len(page.Shards))
		}
		body = map[string]any{"NextToken": page.NextToken, "MaxResults": 2}
	}

	// Then: five shards at two per page took three pages, and every shard
	// arrived exactly once, in shard-ID order
	if pages != 3 {
		t.Errorf("walked %d pages, want 3 (five shards at MaxResults 2)", pages)
	}
	if len(got) != len(want) {
		t.Fatalf("page walk returned %d shards (%v), want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("page walk returned %v, want %v", got, want)
		}
	}
}

func TestListShards_exclusiveStartShardIdResumesAfterThatShard(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "excl-shards", 3)
	ids := openShardIDs(t, srv, "excl-shards", 3)

	page := listShards(t, srv, map[string]any{
		"StreamName": "excl-shards", "ExclusiveStartShardId": ids[0],
	})

	if len(page.Shards) != len(ids)-1 {
		t.Fatalf("got %d shards, want %d", len(page.Shards), len(ids)-1)
	}
	if page.Shards[0].ShardId != ids[1] {
		t.Fatalf("first shard = %q, want %q", page.Shards[0].ShardId, ids[1])
	}
}

// A ListShards NextToken identifies the stream on its own, so AWS refuses a
// request that also names the stream or a start shard.
func TestListShards_nextTokenCombinedWithOtherParametersIsRejected(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "combo", 3)
	ids := openShardIDs(t, srv, "combo", 3)
	first := listShards(t, srv, map[string]any{"StreamName": "combo", "MaxResults": 1})
	if first.NextToken == "" {
		t.Fatal("expected a NextToken from the first page")
	}

	for name, body := range map[string]map[string]any{
		"StreamName":              {"NextToken": first.NextToken, "StreamName": "combo"},
		"ExclusiveStartShardId":   {"NextToken": first.NextToken, "ExclusiveStartShardId": ids[0]},
		"StreamCreationTimestamp": {"NextToken": first.NextToken, "StreamCreationTimestamp": 1700000000},
	} {
		t.Run(name, func(t *testing.T) {
			resp := kinesisCall(t, srv, "ListShards", body)
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidArgumentException")
		})
	}
}

// "When invoking this API, you must use either the StreamARN or the
// StreamName parameter, or both."
func TestListShards_withoutStreamIdentifierIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := kinesisCall(t, srv, "ListShards", map[string]any{})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestListShards_byStreamARN(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "arn-shards", 2)
	arn := kinesisStreamARN(t, srv, "arn-shards")

	page := listShards(t, srv, map[string]any{"StreamARN": arn})

	if len(page.Shards) != 2 {
		t.Fatalf("got %d shards, want 2", len(page.Shards))
	}
}

func TestListShards_malformedNextTokenIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "shard-bad-token", 1)

	resp := kinesisCall(t, srv, "ListShards", map[string]any{"NextToken": "nonsense"})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidArgumentException")
}

func TestRPCv2CBOR_ListShards_paginates(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := kinesisCBORCall(t, srv, "CreateStream", map[string]any{
		"StreamName": "cbor-pages", "ShardCount": 3,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = kinesisCBORCall(t, srv, "ListShards", map[string]any{
		"StreamName": "cbor-pages", "MaxResults": 2,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var first struct {
		Shards []struct {
			ShardId string `cbor:"ShardId"`
		} `cbor:"Shards"`
		NextToken string `cbor:"NextToken"`
	}
	decodeCBOR(t, resp, &first)
	if len(first.Shards) != 2 || first.NextToken == "" {
		t.Fatalf("first CBOR page = %d shards, NextToken %q; want 2 and a token", len(first.Shards), first.NextToken)
	}

	resp = kinesisCBORCall(t, srv, "ListShards", map[string]any{"NextToken": first.NextToken})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var second struct {
		Shards []struct {
			ShardId string `cbor:"ShardId"`
		} `cbor:"Shards"`
		NextToken string `cbor:"NextToken"`
	}
	decodeCBOR(t, resp, &second)
	if len(second.Shards) != 1 || second.NextToken != "" {
		t.Fatalf("second CBOR page = %d shards, NextToken %q; want 1 and no token", len(second.Shards), second.NextToken)
	}
}

// ---- DescribeStream ----------------------------------------------------------

// DescribeStream pages its Shards list with Limit/ExclusiveStartShardId and
// reports truncation through StreamDescription.HasMoreShards.
func TestDescribeStream_limitPagesShardsAndSetsHasMoreShards(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "describe-pages", 3)

	describe := func(body map[string]any) (ids []string, hasMore bool) {
		t.Helper()
		resp := kinesisCall(t, srv, "DescribeStream", body)
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			StreamDescription struct {
				Shards []struct {
					ShardId string `json:"ShardId"`
				} `json:"Shards"`
				HasMoreShards bool `json:"HasMoreShards"`
			} `json:"StreamDescription"`
		}
		decodeJSON(t, resp, &out)
		for _, s := range out.StreamDescription.Shards {
			ids = append(ids, s.ShardId)
		}
		return ids, out.StreamDescription.HasMoreShards
	}

	firstIDs, hasMore := describe(map[string]any{"StreamName": "describe-pages", "Limit": 2})
	if len(firstIDs) != 2 || !hasMore {
		t.Fatalf("first page = %v, HasMoreShards = %v; want 2 shards and true", firstIDs, hasMore)
	}

	secondIDs, hasMore := describe(map[string]any{
		"StreamName": "describe-pages", "Limit": 2, "ExclusiveStartShardId": firstIDs[1],
	})
	if len(secondIDs) != 1 || hasMore {
		t.Fatalf("second page = %v, HasMoreShards = %v; want 1 shard and false", secondIDs, hasMore)
	}
	if secondIDs[0] == firstIDs[0] || secondIDs[0] == firstIDs[1] {
		t.Fatalf("second page repeated a shard from the first: %v then %v", firstIDs, secondIDs)
	}
}

// DescribeStream reports closed shards; ListShards does not. Paging the
// Shards list must not quietly narrow it to the open ones.
func TestDescribeStream_includesTheClosedParentAfterASplit(t *testing.T) {
	// Given: a one-shard stream whose only shard has been split
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "closed-parent", 1)
	parent := openShardIDs(t, srv, "closed-parent", 1)[0]
	resp := kinesisCall(t, srv, "SplitShard", map[string]any{
		"StreamName":         "closed-parent",
		"ShardToSplit":       parent,
		"NewStartingHashKey": "1",
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: the stream is described
	resp = kinesisCall(t, srv, "DescribeStream", map[string]any{"StreamName": "closed-parent"})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StreamDescription struct {
			Shards []struct {
				ShardId             string `json:"ShardId"`
				SequenceNumberRange struct {
					EndingSequenceNumber string `json:"EndingSequenceNumber"`
				} `json:"SequenceNumberRange"`
			} `json:"Shards"`
		} `json:"StreamDescription"`
	}
	decodeJSON(t, resp, &out)

	// Then: the closed parent is there, with the sequence number that closed it
	if len(out.StreamDescription.Shards) != 3 {
		t.Fatalf("DescribeStream returned %d shards, want the parent and its two children", len(out.StreamDescription.Shards))
	}
	for _, shard := range out.StreamDescription.Shards {
		if shard.ShardId != parent {
			continue
		}
		if shard.SequenceNumberRange.EndingSequenceNumber == "" {
			t.Fatalf("parent shard %s has no EndingSequenceNumber", parent)
		}
		return
	}
	t.Fatalf("parent shard %s is missing from DescribeStream", parent)
}

// ---- ListTagsForStream -------------------------------------------------------

// ListTagsForStream pages with Limit/ExclusiveStartTagKey and reports
// truncation through HasMoreTags.
func TestListTagsForStream_limitPagesTagsAndSetsHasMoreTags(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "tag-pages", 1)
	resp := kinesisCall(t, srv, "AddTagsToStream", map[string]any{
		"StreamName": "tag-pages",
		"Tags":       map[string]string{"a": "1", "b": "2", "c": "3"},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	list := func(body map[string]any) (keys []string, hasMore bool) {
		t.Helper()
		resp := kinesisCall(t, srv, "ListTagsForStream", body)
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			Tags []struct {
				Key string `json:"Key"`
			} `json:"Tags"`
			HasMoreTags bool `json:"HasMoreTags"`
		}
		decodeJSON(t, resp, &out)
		for _, tag := range out.Tags {
			keys = append(keys, tag.Key)
		}
		return keys, out.HasMoreTags
	}

	firstKeys, hasMore := list(map[string]any{"StreamName": "tag-pages", "Limit": 2})
	if len(firstKeys) != 2 || !hasMore {
		t.Fatalf("first page = %v, HasMoreTags = %v; want 2 tags and true", firstKeys, hasMore)
	}

	secondKeys, hasMore := list(map[string]any{
		"StreamName": "tag-pages", "Limit": 2, "ExclusiveStartTagKey": firstKeys[1],
	})
	if len(secondKeys) != 1 || hasMore {
		t.Fatalf("second page = %v, HasMoreTags = %v; want 1 tag and false", secondKeys, hasMore)
	}
	if secondKeys[0] != "c" {
		t.Fatalf("second page = %v, want [c]", secondKeys)
	}
}
