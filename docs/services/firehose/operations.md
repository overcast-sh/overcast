---
title: "Firehose operations"
description: "Every Firehose operation Overcast declares — 9 of 9 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - firehose
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# Firehose operations

All 9 listed operations are implemented. Back to [Firehose](../firehose.md).

## Summary

| Category         | ✅ Supported |
| ---------------- | ------------ |
| Delivery Streams | 4            |
| Records          | 2            |
| Tags             | 3            |

---

## Endpoints

### Delivery Streams

| Operation                | Status       | Notes                                                                                                                                                                                       | AWS Docs                                                                                         |
| ------------------------ | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `CreateDeliveryStream`   | ✅ Supported | Creates a delivery stream; destination, Kinesis stream source and encryption configurations are stored and echoed by DescribeDeliveryStream, but nothing is ever delivered to a destination | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_CreateDeliveryStream.html)   |
| `DescribeDeliveryStream` | ✅ Supported | Returns delivery stream details, including the destination(s), stream source and encryption configuration given at creation                                                                 | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_DescribeDeliveryStream.html) |
| `ListDeliveryStreams`    | ✅ Supported | Lists all delivery streams                                                                                                                                                                  | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_ListDeliveryStreams.html)    |
| `DeleteDeliveryStream`   | ✅ Supported | Deletes a delivery stream                                                                                                                                                                   | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_DeleteDeliveryStream.html)   |

### Records

| Operation        | Status       | Notes                                 | AWS Docs                                                                                 |
| ---------------- | ------------ | ------------------------------------- | ---------------------------------------------------------------------------------------- |
| `PutRecord`      | ✅ Supported | Writes a single record to the stream  | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_PutRecord.html)      |
| `PutRecordBatch` | ✅ Supported | Writes multiple records to the stream | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_PutRecordBatch.html) |

### Tags

| Operation                   | Status       | Notes                                        | AWS Docs                                                                                            |
| --------------------------- | ------------ | -------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `TagDeliveryStream`         | ✅ Supported | Adds or overwrites tags on a delivery stream | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_TagDeliveryStream.html)         |
| `UntagDeliveryStream`       | ✅ Supported | Removes tags by key from a delivery stream   | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_UntagDeliveryStream.html)       |
| `ListTagsForDeliveryStream` | ✅ Supported | Returns tags for a delivery stream           | [docs](https://docs.aws.amazon.com/firehose/latest/APIReference/API_ListTagsForDeliveryStream.html) |

## Related

- [Firehose](../firehose.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
