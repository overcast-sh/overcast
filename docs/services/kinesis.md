---
title: "Kinesis — Amazon Kinesis Data Streams"
description: "Kinesis Data Streams with real record storage, partition-key routing, shard iterators and shard split/merge. Retention is recorded but never trims, so records live for the life of the stream."
section: "Service Reference"
tags:
  - amazon
  - data
  - docs
  - kinesis
  - services
  - streams
---

# Kinesis — Amazon Kinesis Data Streams

Records are really stored, routed by partition-key hash and read back through
shard iterators; nothing is ever trimmed.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws kinesis create-stream --stream-name events --shard-count 1
aws kinesis put-record --stream-name events --partition-key a --data hello

ITER=$(aws kinesis get-shard-iterator --stream-name events \
  --shard-id shardId-000000000000 --shard-iterator-type TRIM_HORIZON \
  --query ShardIterator --output text)
aws kinesis get-records --shard-iterator "$ITER"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Streams | `CreateStream` is `ACTIVE` immediately; inline `Tags` and `StreamModeDetails` apply at creation, defaulting to `PROVISIONED` |
| Writes | `PutRecord` and `PutRecords` route by partition-key hash into the owning shard |
| Reads | `GetShardIterator` supports `TRIM_HORIZON`, `LATEST`, `AT_SEQUENCE_NUMBER` and `AFTER_SEQUENCE_NUMBER`; `GetRecords` returns a usable `NextShardIterator` |
| Pagination | `ListStreams`, `ListShards`, `DescribeStream` and `ListTagsForStream` page on their documented cursors and limits; a `NextToken` expires after 300 seconds |
| Resharding | `SplitShard` and `MergeShards` close the parents and create children that name them in `ParentShardId`/`AdjacentParentShardId`; the closed parents stay listed with an `EndingSequenceNumber`, and `MergeShards` refuses shards that are not adjacent |
| Shard listing | `ListShards` honours every `ShardFilter` type; a `NextToken` keeps the filter it was issued under |
| Consumers | Lambda event source mappings and EventBridge Pipes poll Kinesis streams |
| Tags | `AddTagsToStream`/`RemoveTagsFromStream` and the ARN-addressed `TagResource`/`UntagResource`/`ListTagsForResource` |

## Differences from AWS

| Area             | On AWS                                                             | Overcast                                                                                                                                                                         |
| ---------------- | ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Retention        | Records expire at the retention period                             | `IncreaseStreamRetentionPeriod` and `DecreaseStreamRetentionPeriod` store and echo the value; no record is ever expired, so a shard keeps everything until the stream is deleted |
| Throttling       | `ProvisionedThroughputExceededException` past the provisioned rate | `PutRecords` always reports `FailedRecordCount: 0`; throughput throttling is not simulated                                                                                       |
| Encryption       | Records are encrypted with the named key                           | `StartStreamEncryption` stores `EncryptionType` and `KeyId` and `Describe*` echoes them; records are stored unencrypted                                                          |
| Capacity modes   | On-demand capacity is enforced                                     | `UpdateStreamMode` is recorded; nothing is enforced                                                                                                                              |
| Shard expiry     | A closed shard expires with its records and leaves the listings    | Closed shards never expire, since no record does: `ListShards` and `DescribeStream` keep every split or merge parent for the life of the stream                                  |
| Enhanced fan-out | `SubscribeToShard` and the consumer registration APIs              | Not emulated, and a consumer ARN is refused by `TagResource`                                                                                                                     |

## Gotchas

> [!WARNING]
> Because nothing is trimmed, a long-lived stream in a shared Overcast keeps
> growing and `TRIM_HORIZON` keeps replaying from the very first record. Delete
> and re-create the stream between test runs — `DeleteStream` removes its
> records with it.

<!-- BEGIN overcast:capabilities -->

## Operations

All 23 listed operations are implemented.
Per-operation status, notes and AWS API links: [Kinesis operations](kinesis/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Firehose](./firehose.md)
- [DynamoDB Streams](./dynamodbstreams.md)
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/kinesis/latest/APIReference/Welcome.html)
