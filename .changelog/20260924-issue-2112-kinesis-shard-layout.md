* [kinesis] `CreateStream` splits the hash keyspace at floor(i·2^128/N), so a 2-shard stream's shard 0 ends at 2^127−1 as on AWS (#2112).
  the MCP create-stream tool, which gave every shard the whole keyspace, now lays shards out the same way.
*! [kinesis] `SplitShard` children no longer share the split key, and `NewStartingHashKey` outside the parent's range is refused (#2112).
  the lower child ends at `NewStartingHashKey` − 1; a key at or beyond either end of the parent is `InvalidArgumentException`.
  migration: pick a split key strictly between the parent's `StartingHashKey` and `EndingHashKey`, e.g. their midpoint.
*! [kinesis] `MergeShards` refuses two shards whose hash key ranges are not adjacent with `InvalidArgumentException`, as AWS does (#2112).
  migration: merge neighbours — shards where one's `EndingHashKey` + 1 is the other's `StartingHashKey`.
* [kinesis] split and merge children carry `ParentShardId`/`AdjacentParentShardId` in `DescribeStream` and `ListShards` (#2112).
*! [kinesis] `ListShards` lists closed split/merge parents with their `EndingSequenceNumber` and honours `ShardFilter` (#2112).
  migration: pass `ShardFilter: {Type: AT_LATEST}`, or skip shards with an `EndingSequenceNumber`, to list only open shards.
