---
title: "Kinesis operations"
description: "Every Kinesis operation Overcast declares — 23 of 23 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - kinesis
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# Kinesis operations

All 23 listed operations are implemented. Back to [Kinesis](../kinesis.md).

## Summary

| Category | ✅ Supported |
| -------- | ------------ |
| General  | 23           |

---

## Endpoints

### General

| Operation                       | Status       | Notes                                                                                                                                                                                                                                                    | AWS Docs                                                                                               |
| ------------------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `AddTagsToStream`               | ✅ Supported |                                                                                                                                                                                                                                                          | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_AddTagsToStream.html)               |
| `CreateStream`                  | ✅ Supported | Stream becomes ACTIVE immediately; inline `Tags` and `StreamModeDetails` applied at creation, defaulting to PROVISIONED                                                                                                                                  | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_CreateStream.html)                  |
| `DecreaseStreamRetentionPeriod` | ✅ Supported | Stores and echoes the new value; does not trim any record now older than the shortened window                                                                                                                                                            | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DecreaseStreamRetentionPeriod.html) |
| `DeleteStream`                  | ✅ Supported | Also removes all stored records                                                                                                                                                                                                                          | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DeleteStream.html)                  |
| `DescribeStream`                | ✅ Supported | Pages the Shards list on Limit/ExclusiveStartShardId and reports HasMoreShards                                                                                                                                                                           | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStream.html)                |
| `DescribeStreamSummary`         | ✅ Supported | Lightweight summary without shard detail                                                                                                                                                                                                                 | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStreamSummary.html)         |
| `GetRecords`                    | ✅ Supported | Returns stored records and a valid NextShardIterator; records are never expired by RetentionPeriodHours, so a shard keeps every record for the life of the stream regardless of the configured retention                                                 | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetRecords.html)                    |
| `GetShardIterator`              | ✅ Supported | Supports TRIM_HORIZON, LATEST, AT/AFTER_SEQUENCE_NUMBER                                                                                                                                                                                                  | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetShardIterator.html)              |
| `IncreaseStreamRetentionPeriod` | ✅ Supported | Stores and echoes the new value from DescribeStream/DescribeStreamSummary; not enforced against stored records (see GetRecords)                                                                                                                          | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_IncreaseStreamRetentionPeriod.html) |
| `ListShards`                    | ✅ Supported | Paginates on MaxResults/NextToken/ExclusiveStartShardId, addressable by StreamName or StreamARN; lists closed split/merge parents with their lineage and honours every ShardFilter type. Closed shards never expire, so FROM_TRIM_HORIZON is every shard | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html)                    |
| `ListStreams`                   | ✅ Supported | Paginates on Limit/NextToken/ExclusiveStartStreamName and returns StreamSummaries alongside StreamNames                                                                                                                                                  | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListStreams.html)                   |
| `ListTagsForResource`           | ✅ Supported | Stream ARNs; the same tag set `ListTagsForStream` returns                                                                                                                                                                                                | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListTagsForResource.html)           |
| `ListTagsForStream`             | ✅ Supported | Pages on Limit/ExclusiveStartTagKey and reports HasMoreTags                                                                                                                                                                                              | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListTagsForStream.html)             |
| `MergeShards`                   | ✅ Supported | Refuses shards whose hash key ranges are not adjacent; closes both parents and creates a child naming them as ParentShardId/AdjacentParentShardId                                                                                                        | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_MergeShards.html)                   |
| `PutRecord`                     | ✅ Supported | Routes by partition key hash                                                                                                                                                                                                                             | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecord.html)                     |
| `PutRecords`                    | ✅ Supported | Returns FailedRecordCount=0 for all records                                                                                                                                                                                                              | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecords.html)                    |
| `RemoveTagsFromStream`          | ✅ Supported |                                                                                                                                                                                                                                                          | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_RemoveTagsFromStream.html)          |
| `SplitShard`                    | ✅ Supported | NewStartingHashKey must lie strictly inside the parent's range; closes the parent and creates two children, split at that key, naming it as ParentShardId                                                                                                | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_SplitShard.html)                    |
| `StartStreamEncryption`         | ✅ Supported | Stores EncryptionType/KeyId and echoes them from Describe*; records are not actually encrypted at rest                                                                                                                                                   | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_StartStreamEncryption.html)         |
| `StopStreamEncryption`          | ✅ Supported | Resets EncryptionType to NONE and clears KeyId                                                                                                                                                                                                           | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_StopStreamEncryption.html)          |
| `TagResource`                   | ✅ Supported | Stream ARNs; consumer ARNs are rejected because consumers are not emulated                                                                                                                                                                               | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_TagResource.html)                   |
| `UntagResource`                 | ✅ Supported | Stream ARNs                                                                                                                                                                                                                                              | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_UntagResource.html)                 |
| `UpdateStreamMode`              | ✅ Supported | Stores StreamModeDetails and echoes it from Describe*; on-demand capacity is not actually enforced                                                                                                                                                       | [docs](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_UpdateStreamMode.html)              |

## Related

- [Kinesis](../kinesis.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
