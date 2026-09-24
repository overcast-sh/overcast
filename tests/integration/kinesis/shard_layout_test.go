// shard_layout_test.go pins how Kinesis deals the 128-bit hash keyspace out
// to shards and how SplitShard/MergeShards reshape it (#2112):
//
//   - CreateStream gives shard i of N the range starting at floor(i·2^128/N).
//     AWS's list-shards CLI example (a 3-shard stream whose shardId-000000000001
//     starts at 113427455640312821154458202477256070485 = floor(2^128/3)) is
//     the evidence; a 2-shard stream's shard 0 therefore ends at 2^127−1.
//   - SplitShard's lower child ends one below NewStartingHashKey, and the key
//     must lie strictly inside the parent's range.
//   - Children name their parents (ParentShardId, and AdjacentParentShardId
//     for a merge), and the closed parents stay listed with an
//     EndingSequenceNumber.
//   - MergeShards refuses two shards whose ranges do not form one contiguous
//     set.
//   - ListShards honours ShardFilter.
//
// AWS references:
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_SplitShard.html
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_MergeShards.html
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_Shard.html
//   - https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ShardFilter.html
//   - https://docs.aws.amazon.com/cli/latest/reference/kinesis/list-shards.html
package kinesis_test

import (
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// wireShard is one element of ListShards' Shards or DescribeStream's
// StreamDescription.Shards.
type wireShard struct {
	ShardId               string `json:"ShardId"`
	ParentShardId         string `json:"ParentShardId"`
	AdjacentParentShardId string `json:"AdjacentParentShardId"`
	HashKeyRange          struct {
		StartingHashKey string `json:"StartingHashKey"`
		EndingHashKey   string `json:"EndingHashKey"`
	} `json:"HashKeyRange"`
	SequenceNumberRange struct {
		StartingSequenceNumber string `json:"StartingSequenceNumber"`
		EndingSequenceNumber   string `json:"EndingSequenceNumber"`
	} `json:"SequenceNumberRange"`
}

func listWireShards(t *testing.T, srv *helpers.TestServer, body map[string]any) []wireShard {
	t.Helper()
	resp := kinesisCall(t, srv, "ListShards", body)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Shards []wireShard `json:"Shards"`
	}
	decodeJSON(t, resp, &out)
	return out.Shards
}

func describeWireShards(t *testing.T, srv *helpers.TestServer, stream string) []wireShard {
	t.Helper()
	resp := kinesisCall(t, srv, "DescribeStream", map[string]any{"StreamName": stream})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		StreamDescription struct {
			Shards []wireShard `json:"Shards"`
		} `json:"StreamDescription"`
	}
	decodeJSON(t, resp, &out)
	return out.StreamDescription.Shards
}

func shardIDs(shards []wireShard) []string {
	ids := make([]string, len(shards))
	for i, s := range shards {
		ids[i] = s.ShardId
	}
	return ids
}

func shardByID(t *testing.T, shards []wireShard, id string) wireShard {
	t.Helper()
	for _, s := range shards {
		if s.ShardId == id {
			return s
		}
	}
	t.Fatalf("shard %s not listed among %v", id, shardIDs(shards))
	return wireShard{}
}

func assertShardIDs(t *testing.T, what string, got []wireShard, want ...string) {
	t.Helper()
	ids := shardIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("%s = %v, want %v", what, ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, ids, want)
		}
	}
}

func splitShard(t *testing.T, srv *helpers.TestServer, stream, shard, key string) {
	t.Helper()
	resp := kinesisCall(t, srv, "SplitShard", map[string]any{
		"StreamName": stream, "ShardToSplit": shard, "NewStartingHashKey": key,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func mergeShards(t *testing.T, srv *helpers.TestServer, stream, shard, adjacent string) {
	t.Helper()
	resp := kinesisCall(t, srv, "MergeShards", map[string]any{
		"StreamName": stream, "ShardToMerge": shard, "AdjacentShardToMerge": adjacent,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// keyspaceBoundary is floor(i·2^128/n), the first hash key of shard i of n.
func keyspaceBoundary(i, n int64) *big.Int {
	b := new(big.Int).Lsh(big.NewInt(1), 128)
	b.Mul(b, big.NewInt(i))
	return b.Div(b, big.NewInt(n))
}

// ---- CreateStream ------------------------------------------------------------

func TestCreateStream_hashKeyRangesSplitTheKeyspaceAtFloorOfITimes2To128OverN(t *testing.T) {
	for _, n := range []int64{1, 2, 3, 4, 7} {
		t.Run(big.NewInt(n).String()+" shards", func(t *testing.T) {
			// Given: a stream with n shards
			srv := helpers.NewTestServer(t)
			createStream(t, srv, "layout", int(n))

			// When: its shards are listed
			shards := listWireShards(t, srv, map[string]any{"StreamName": "layout"})

			// Then: shard i covers [floor(i·2^128/n), floor((i+1)·2^128/n) − 1]
			if int64(len(shards)) != n {
				t.Fatalf("got %d shards, want %d", len(shards), n)
			}
			for i, s := range shards {
				wantStart := keyspaceBoundary(int64(i), n)
				wantEnd := new(big.Int).Sub(keyspaceBoundary(int64(i)+1, n), big.NewInt(1))
				if s.HashKeyRange.StartingHashKey != wantStart.String() || s.HashKeyRange.EndingHashKey != wantEnd.String() {
					t.Fatalf("shard %d range = [%s, %s], want [%s, %s]", i,
						s.HashKeyRange.StartingHashKey, s.HashKeyRange.EndingHashKey, wantStart, wantEnd)
				}
			}
		})
	}
}

// The AWS CLI reference's list-shards example is a 3-shard stream; its
// shards 1 and 2 are reproduced exactly.
func TestCreateStream_threeShardRangesMatchTheAWSCLIExample(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "samplestream", 3)

	shards := listWireShards(t, srv, map[string]any{"StreamName": "samplestream", "ExclusiveStartShardId": "shardId-000000000000"})

	want := [][2]string{
		{"113427455640312821154458202477256070485", "226854911280625642308916404954512140969"},
		{"226854911280625642308916404954512140970", "340282366920938463463374607431768211455"},
	}
	if len(shards) != len(want) {
		t.Fatalf("got %d shards, want %d", len(shards), len(want))
	}
	for i, w := range want {
		if shards[i].HashKeyRange.StartingHashKey != w[0] || shards[i].HashKeyRange.EndingHashKey != w[1] {
			t.Fatalf("shard %s range = [%s, %s], want [%s, %s]", shards[i].ShardId,
				shards[i].HashKeyRange.StartingHashKey, shards[i].HashKeyRange.EndingHashKey, w[0], w[1])
		}
	}
}

// ---- SplitShard --------------------------------------------------------------

func TestSplitShard_childrenAbutAtTheNewStartingHashKeyAndNameTheirParent(t *testing.T) {
	// Given: a one-shard stream
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "split-lineage", 1)

	// When: shard 0 is split at 1000
	splitShard(t, srv, "split-lineage", "shardId-000000000000", "1000")

	// Then: ListShards lists the closed parent and both children
	shards := listWireShards(t, srv, map[string]any{"StreamName": "split-lineage"})
	assertShardIDs(t, "ListShards after split", shards, "shardId-000000000000", "shardId-000000000001", "shardId-000000000002")

	parent := shards[0]
	if parent.SequenceNumberRange.EndingSequenceNumber == "" {
		t.Fatalf("closed parent has no EndingSequenceNumber: %+v", parent)
	}
	if parent.ParentShardId != "" || parent.AdjacentParentShardId != "" {
		t.Fatalf("an initial shard has no parent, got %+v", parent)
	}

	lower, upper := shards[1], shards[2]
	if lower.HashKeyRange.StartingHashKey != "0" || lower.HashKeyRange.EndingHashKey != "999" {
		t.Fatalf("lower child range = [%s, %s], want [0, 999]", lower.HashKeyRange.StartingHashKey, lower.HashKeyRange.EndingHashKey)
	}
	if upper.HashKeyRange.StartingHashKey != "1000" || upper.HashKeyRange.EndingHashKey != "340282366920938463463374607431768211455" {
		t.Fatalf("upper child range = [%s, %s], want [1000, 2^128-1]", upper.HashKeyRange.StartingHashKey, upper.HashKeyRange.EndingHashKey)
	}
	for _, child := range []wireShard{lower, upper} {
		if child.ParentShardId != "shardId-000000000000" {
			t.Fatalf("child %s ParentShardId = %q, want shardId-000000000000", child.ShardId, child.ParentShardId)
		}
		if child.AdjacentParentShardId != "" {
			t.Fatalf("split child %s has AdjacentParentShardId %q, want none", child.ShardId, child.AdjacentParentShardId)
		}
		if child.SequenceNumberRange.EndingSequenceNumber != "" {
			t.Fatalf("child %s is closed: %+v", child.ShardId, child)
		}
	}

	// And: DescribeStream reports the same lineage
	described := describeWireShards(t, srv, "split-lineage")
	assertShardIDs(t, "DescribeStream after split", described, "shardId-000000000000", "shardId-000000000001", "shardId-000000000002")
	if got := shardByID(t, described, "shardId-000000000002").ParentShardId; got != "shardId-000000000000" {
		t.Fatalf("DescribeStream child ParentShardId = %q, want shardId-000000000000", got)
	}
}

// A record whose explicit hash key is the split point lands on the upper
// child only — the two children no longer share it.
func TestSplitShard_theSplitKeyBelongsToTheUpperChildOnly(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "split-key", 1)
	splitShard(t, srv, "split-key", "shardId-000000000000", "1000")

	for key, want := range map[string]string{"999": "shardId-000000000001", "1000": "shardId-000000000002"} {
		resp := kinesisCall(t, srv, "PutRecord", map[string]any{
			"StreamName": "split-key", "Data": []byte("x"), "PartitionKey": "pk", "ExplicitHashKey": key,
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var out struct {
			ShardId string `json:"ShardId"`
		}
		decodeJSON(t, resp, &out)
		if out.ShardId != want {
			t.Fatalf("ExplicitHashKey %s placed on %s, want %s", key, out.ShardId, want)
		}
	}
}

func TestSplitShard_newStartingHashKeyOutsideTheParentIsInvalidArgument(t *testing.T) {
	// Given: a two-shard stream; shard 0 covers [0, 2^127−1]
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "split-bad-key", 2)
	shard0End := new(big.Int).Sub(keyspaceBoundary(1, 2), big.NewInt(1)).String()

	for name, key := range map[string]string{
		"the parent's first key (empty lower child)": "0",
		"the parent's last key":                      shard0End,
		"past the parent's range":                    keyspaceBoundary(1, 2).String(),
		"not a number":                               "ten",
		"negative":                                   "-5",
		"empty":                                      "",
	} {
		t.Run(name, func(t *testing.T) {
			assertKinesisError(t, srv, "SplitShard", map[string]any{
				"StreamName": "split-bad-key", "ShardToSplit": "shardId-000000000000", "NewStartingHashKey": key,
			}, "InvalidArgumentException", http.StatusBadRequest)
		})
	}

	// And: nothing was split
	if got := listWireShards(t, srv, map[string]any{"StreamName": "split-bad-key"}); len(got) != 2 {
		t.Fatalf("a rejected split changed the stream: %v", shardIDs(got))
	}
}

// ---- MergeShards -------------------------------------------------------------

func TestMergeShards_mergedChildNamesBothParentsAndCoversTheirUnion(t *testing.T) {
	// Given: a three-shard stream
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "merge-lineage", 3)

	// When: shard 2 is merged with its lower neighbour, shard 1 — named in
	// that order, so ShardToMerge is the higher range
	mergeShards(t, srv, "merge-lineage", "shardId-000000000002", "shardId-000000000001")

	// Then: both parents stay listed, closed; the child names ShardToMerge as
	// its parent and AdjacentShardToMerge as the adjacent parent
	shards := listWireShards(t, srv, map[string]any{"StreamName": "merge-lineage"})
	assertShardIDs(t, "ListShards after merge", shards,
		"shardId-000000000000", "shardId-000000000001", "shardId-000000000002", "shardId-000000000003")
	for _, id := range []string{"shardId-000000000001", "shardId-000000000002"} {
		if shardByID(t, shards, id).SequenceNumberRange.EndingSequenceNumber == "" {
			t.Fatalf("merged parent %s has no EndingSequenceNumber", id)
		}
	}
	child := shards[3]
	if child.ParentShardId != "shardId-000000000002" || child.AdjacentParentShardId != "shardId-000000000001" {
		t.Fatalf("merged child lineage = (Parent %q, AdjacentParent %q), want (shardId-000000000002, shardId-000000000001)",
			child.ParentShardId, child.AdjacentParentShardId)
	}
	if child.HashKeyRange.StartingHashKey != keyspaceBoundary(1, 3).String() ||
		child.HashKeyRange.EndingHashKey != "340282366920938463463374607431768211455" {
		t.Fatalf("merged child range = [%s, %s], want [floor(2^128/3), 2^128-1]",
			child.HashKeyRange.StartingHashKey, child.HashKeyRange.EndingHashKey)
	}
}

func TestMergeShards_nonAdjacentShardsAreInvalidArgument(t *testing.T) {
	// Given: a three-shard stream, whose shards 0 and 2 are not adjacent
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "merge-gap", 3)

	for name, pair := range map[string][2]string{
		"a gap between them":  {"shardId-000000000000", "shardId-000000000002"},
		"a shard with itself": {"shardId-000000000001", "shardId-000000000001"},
	} {
		t.Run(name, func(t *testing.T) {
			// When: they are merged
			// Then: AWS's InvalidArgumentException
			assertKinesisError(t, srv, "MergeShards", map[string]any{
				"StreamName": "merge-gap", "ShardToMerge": pair[0], "AdjacentShardToMerge": pair[1],
			}, "InvalidArgumentException", http.StatusBadRequest)
		})
	}

	// And: every shard is still open
	if got := listWireShards(t, srv, map[string]any{"StreamName": "merge-gap", "ShardFilter": map[string]any{"Type": "AT_LATEST"}}); len(got) != 3 {
		t.Fatalf("a rejected merge changed the open shards: %v", shardIDs(got))
	}
}

// After a split, the unsplit neighbour and the far child are not adjacent —
// the pair every native kinesis-shards compat test used to merge.
func TestMergeShards_afterASplitOnlyTheAbuttingShardsMerge(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "merge-after-split", 2)
	splitShard(t, srv, "merge-after-split", "shardId-000000000000", "85070591730234615865843651857942052863")

	// shard 1 is [2^127, 2^128−1]; child 2 is [0, 2^126−2]: a gap.
	assertKinesisError(t, srv, "MergeShards", map[string]any{
		"StreamName": "merge-after-split", "ShardToMerge": "shardId-000000000001", "AdjacentShardToMerge": "shardId-000000000002",
	}, "InvalidArgumentException", http.StatusBadRequest)

	// child 3 is [2^126−1, 2^127−1] and abuts shard 1.
	mergeShards(t, srv, "merge-after-split", "shardId-000000000003", "shardId-000000000001")
	open := listWireShards(t, srv, map[string]any{"StreamName": "merge-after-split", "ShardFilter": map[string]any{"Type": "AT_LATEST"}})
	assertShardIDs(t, "open shards", open, "shardId-000000000002", "shardId-000000000004")
	if got := open[1].HashKeyRange.StartingHashKey; got != "85070591730234615865843651857942052863" {
		t.Fatalf("merged child StartingHashKey = %s, want the split key", got)
	}
}

// ---- ListShards ShardFilter --------------------------------------------------

// reshardedStream builds, on a mock clock, a stream with every kind of shard
// a filter distinguishes:
//
//	t=0    CreateStream, 2 shards            → 0, 1
//	t=100s SplitShard 0                      → 0 closed; 2, 3 open
//	t=200s MergeShards 3 + 1                 → 1, 3 closed; 4 open
//
// Final open shards: 2, 4.
func reshardedStream(t *testing.T) *helpers.TestServer {
	t.Helper()
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	createStream(t, srv, "filtered", 2)
	srv.AdvanceClock(100 * time.Second)
	splitShard(t, srv, "filtered", "shardId-000000000000", "1000")
	srv.AdvanceClock(100 * time.Second)
	mergeShards(t, srv, "filtered", "shardId-000000000003", "shardId-000000000001")
	srv.AdvanceClock(100 * time.Second)
	return srv
}

func TestListShards_shardFilterSelectsShardsByLifecycle(t *testing.T) {
	srv := reshardedStream(t)

	for _, tc := range []struct {
		name   string
		filter map[string]any
		want   []string
	}{
		{"no filter lists every shard", nil,
			[]string{"shardId-000000000000", "shardId-000000000001", "shardId-000000000002", "shardId-000000000003", "shardId-000000000004"}},
		{"FROM_TRIM_HORIZON lists every shard in retention", map[string]any{"Type": "FROM_TRIM_HORIZON"},
			[]string{"shardId-000000000000", "shardId-000000000001", "shardId-000000000002", "shardId-000000000003", "shardId-000000000004"}},
		{"AT_TRIM_HORIZON lists the shards open at the stream's start", map[string]any{"Type": "AT_TRIM_HORIZON"},
			[]string{"shardId-000000000000", "shardId-000000000001"}},
		{"AT_LATEST lists the open shards", map[string]any{"Type": "AT_LATEST"},
			[]string{"shardId-000000000002", "shardId-000000000004"}},
		{"AFTER_SHARD_ID resumes after the named shard", map[string]any{"Type": "AFTER_SHARD_ID", "ShardId": "shardId-000000000002"},
			[]string{"shardId-000000000003", "shardId-000000000004"}},
		{"AT_TIMESTAMP between the reshards lists what was open then", map[string]any{"Type": "AT_TIMESTAMP", "Timestamp": 150},
			[]string{"shardId-000000000001", "shardId-000000000002", "shardId-000000000003"}},
		{"AT_TIMESTAMP at a split includes the closing parent and its children", map[string]any{"Type": "AT_TIMESTAMP", "Timestamp": 100},
			[]string{"shardId-000000000000", "shardId-000000000001", "shardId-000000000002", "shardId-000000000003"}},
		{"AT_TIMESTAMP before the stream existed is the trim horizon", map[string]any{"Type": "AT_TIMESTAMP", "Timestamp": -50},
			[]string{"shardId-000000000000", "shardId-000000000001"}},
		{"FROM_TIMESTAMP drops shards closed before it", map[string]any{"Type": "FROM_TIMESTAMP", "Timestamp": 150},
			[]string{"shardId-000000000001", "shardId-000000000002", "shardId-000000000003", "shardId-000000000004"}},
		{"FROM_TIMESTAMP after every reshard lists the open shards", map[string]any{"Type": "FROM_TIMESTAMP", "Timestamp": 250.5},
			[]string{"shardId-000000000002", "shardId-000000000004"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"StreamName": "filtered"}
			if tc.filter != nil {
				body["ShardFilter"] = tc.filter
			}
			assertShardIDs(t, "ListShards", listWireShards(t, srv, body), tc.want...)
		})
	}
}

// A NextToken carries the filter it was minted under: the second page is
// still filtered even though the request may not repeat ShardFilter.
func TestListShards_nextTokenKeepsTheShardFilter(t *testing.T) {
	srv := reshardedStream(t)

	first := listShards(t, srv, map[string]any{
		"StreamName": "filtered", "MaxResults": 1, "ShardFilter": map[string]any{"Type": "AT_LATEST"},
	})
	if len(first.Shards) != 1 || first.Shards[0].ShardId != "shardId-000000000002" || first.NextToken == "" {
		t.Fatalf("first page = %+v, want shardId-000000000002 and a NextToken", first)
	}
	second := listShards(t, srv, map[string]any{"NextToken": first.NextToken})
	if len(second.Shards) != 1 || second.Shards[0].ShardId != "shardId-000000000004" || second.NextToken != "" {
		t.Fatalf("second page = %+v, want only shardId-000000000004 and no NextToken", second)
	}
}

func TestListShards_malformedShardFilterIsInvalidArgument(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createStream(t, srv, "bad-filter", 2)

	for name, filter := range map[string]map[string]any{
		"no Type":                          {},
		"an unknown Type":                  {"Type": "EVERYTHING"},
		"AFTER_SHARD_ID without ShardId":   {"Type": "AFTER_SHARD_ID"},
		"AT_TIMESTAMP without Timestamp":   {"Type": "AT_TIMESTAMP"},
		"FROM_TIMESTAMP without Timestamp": {"Type": "FROM_TIMESTAMP"},
		"ShardId on AT_LATEST":             {"Type": "AT_LATEST", "ShardId": "shardId-000000000000"},
		"Timestamp on AT_TRIM_HORIZON":     {"Type": "AT_TRIM_HORIZON", "Timestamp": 1},
	} {
		t.Run(name, func(t *testing.T) {
			assertKinesisError(t, srv, "ListShards", map[string]any{"StreamName": "bad-filter", "ShardFilter": filter},
				"InvalidArgumentException", http.StatusBadRequest)
		})
	}

	t.Run("ShardFilter with a NextToken", func(t *testing.T) {
		first := listShards(t, srv, map[string]any{"StreamName": "bad-filter", "MaxResults": 1})
		assertKinesisError(t, srv, "ListShards", map[string]any{
			"NextToken": first.NextToken, "ShardFilter": map[string]any{"Type": "AT_LATEST"},
		}, "InvalidArgumentException", http.StatusBadRequest)
	})
}

// The rpc-v2-cbor door decodes ShardFilter the same way, including a
// Timestamp sent as a CBOR epoch-seconds tag, as the SDKs that speak CBOR do.
func TestRPCv2CBOR_ListShards_honoursShardFilter(t *testing.T) {
	srv := reshardedStream(t)

	for name, filter := range map[string]map[string]any{
		"AT_LATEST":    {"Type": "AT_LATEST"},
		"AT_TIMESTAMP": {"Type": "AT_TIMESTAMP", "Timestamp": time.Unix(250, 0).UTC()},
	} {
		t.Run(name, func(t *testing.T) {
			resp := kinesisCBORCall(t, srv, "ListShards", map[string]any{"StreamName": "filtered", "ShardFilter": filter})
			helpers.AssertStatus(t, resp, http.StatusOK)
			var out struct {
				Shards []struct {
					ShardId string `cbor:"ShardId"`
				} `cbor:"Shards"`
			}
			decodeCBOR(t, resp, &out)
			if len(out.Shards) != 2 || out.Shards[0].ShardId != "shardId-000000000002" || out.Shards[1].ShardId != "shardId-000000000004" {
				t.Fatalf("CBOR ListShards %s = %+v, want shardId-000000000002 and shardId-000000000004", name, out.Shards)
			}
		})
	}
}
