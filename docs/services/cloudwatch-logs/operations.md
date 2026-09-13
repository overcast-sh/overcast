---
title: "CloudWatch Logs operations"
description: "Every CloudWatch Logs operation Overcast declares — 23 of 26 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - cloudwatch
  - docs
  - logs
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# CloudWatch Logs operations

23 of 26 listed operations are implemented. Back to [CloudWatch Logs](../cloudwatch-logs.md).

## Summary

| Category       | ✅ Supported | ⚠️ Partial | ❌ Unsupported |
| -------------- | ------------ | ---------- | -------------- |
| Log groups     | 3            |            |                |
| Log streams    | 3            |            |                |
| Log events     | 4            | 1          |                |
| Insights       |              |            | 2              |
| Metric filters | 4            |            |                |
| Retention      | 2            |            | 1              |
| Tagging        | 6            |            |                |

---

## Endpoints

### Log groups

| Operation           | Status       | Notes                                                                                                                                                                                   | AWS Docs                                                                                                |
| ------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `CreateLogGroup`    | ✅ Supported | Validates name; returns error on duplicate; applies create-time `tags` atomically with the group (`kmsKeyId`, `logGroupClass` and `deletionProtectionEnabled` are accepted but ignored) | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_CreateLogGroup.html)    |
| `DescribeLogGroups` | ✅ Supported | Optional `logGroupNamePrefix` filter; `limit` (default and maximum 50) and `nextToken` page the ASCII-sorted result                                                                     | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeLogGroups.html) |
| `DeleteLogGroup`    | ✅ Supported | Deletes group and all streams/events                                                                                                                                                    | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DeleteLogGroup.html)    |

### Log streams

| Operation            | Status       | Notes                                                                                                                                                     | AWS Docs                                                                                                 |
| -------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `CreateLogStream`    | ✅ Supported | Validates group exists; returns error on duplicate                                                                                                        | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_CreateLogStream.html)    |
| `DescribeLogStreams` | ✅ Supported | Optional `logStreamNamePrefix` filter; `limit` (default and maximum 50) and `nextToken` page the result (`orderBy`/`descending` are accepted but ignored) | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeLogStreams.html) |
| `DeleteLogStream`    | ✅ Supported | Deletes stream and all its events                                                                                                                         | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DeleteLogStream.html)    |

### Log events

| Operation         | Status       | Notes                                                                                                                                                                                                                                                                                                                           | AWS Docs                                                                                              |
| ----------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `PutLogEvents`    | ✅ Supported | Accepts a batch of events and sets ingestion time; an event more than 2 hours ahead, older than 14 days or older than the group's retention is discarded and reported in `rejectedLogEventsInfo` behind a 200; a batch that is not in chronological order is refused with `InvalidParameterException`                           | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutLogEvents.html)    |
| `GetLogEvents`    | ✅ Supported | startTime/endTime filtering; startFromHead                                                                                                                                                                                                                                                                                      | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html)    |
| `FilterLogEvents` | ✅ Supported | Text patterns (AND, quoted, ?OR), JSON patterns (`{ $.field op value }` with `&&`/`\|\|`, EXISTS, IS NULL), space-delimited patterns (`[col, col = val, ...]` with `*` glob, `%regex%`, numeric ops, `&&`/`\|\|`, ellipsis); time range, stream name/prefix; each event carries an `eventId` that resolves through GetLogRecord | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html) |
| `GetLogRecord`    | ⚠️ Partial   | Resolves an `eventId` returned by FilterLogEvents to that event's `@message`, `@timestamp`, `@ingestionTime`, `@log` and `@logStream`; a Logs Insights `@ptr` cannot be produced here because StartQuery is unimplemented, and a structured message is not split into per-field entries                                         | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogRecord.html)    |
| `StartLiveTail`   | ✅ Supported | AWS event-stream response opening with initial-response, then sessionStart/sessionUpdate; supports group identifiers, stream names/prefixes, and filter patterns                                                                                                                                                                | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_StartLiveTail.html)   |

### Insights

| Operation         | Status         | Notes             | AWS Docs                                                                                              |
| ----------------- | -------------- | ----------------- | ----------------------------------------------------------------------------------------------------- |
| `StartQuery`      | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_StartQuery.html)      |
| `GetQueryResults` | ❌ Unsupported | stub; returns 501 | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetQueryResults.html) |

### Metric filters

| Operation               | Status       | Notes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | AWS Docs                                                                                                    |
| ----------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `PutMetricFilter`       | ✅ Supported | Creates or replaces a filter (100 per group); every accepted log event, from `PutLogEvents` or a Lambda function's output, that matches the pattern publishes a CloudWatch datapoint with the transformation's namespace, name, `metricValue` (a number or a `$field`/`$.field` reference), `dimensions` and `unit`, at the event's timestamp; `defaultValue` is published once per one-minute period that ingested events but none that matched, decided per batch; `applyOnTransformedLogs` is stored and ignored (no log transformers); nothing is published while service metrics are disabled | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutMetricFilter.html)       |
| `DescribeMetricFilters` | ✅ Supported | Optional `logGroupName`, `filterNamePrefix` (honoured only with `logGroupName`), and `metricName` + `metricNamespace` (required together); `limit` (default and maximum 50) and `nextToken` page the name-sorted result                                                                                                                                                                                                                                                                                                                                                                            | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DescribeMetricFilters.html) |
| `DeleteMetricFilter`    | ✅ Supported | Deletes one filter; deleting a log group deletes its filters                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DeleteMetricFilter.html)    |
| `TestMetricFilter`      | ✅ Supported | Runs a pattern over 1–50 sample messages; `matches` carries the zero-based `eventNumber`, the message and `extractedValues` — every column of a space-delimited pattern (unnamed ones as `$1`…), the properties a JSON pattern selects (AWS documents no JSON example; this mirrors the space-delimited rule), or `{}` for a text pattern                                                                                                                                                                                                                                                          | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html)      |

### Retention

| Operation               | Status         | Notes                                                                                                                                        | AWS Docs                                                                                                    |
| ----------------------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `PutRetentionPolicy`    | ✅ Supported   | Sets retentionInDays on log group; values outside AWS's documented set are rejected with `InvalidParameterException` before any state change | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutRetentionPolicy.html)    |
| `DeleteRetentionPolicy` | ✅ Supported   | Clears retention (sets to 0)                                                                                                                 | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_DeleteRetentionPolicy.html) |
| `PutSubscriptionFilter` | ❌ Unsupported | stub; returns 501                                                                                                                            | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutSubscriptionFilter.html) |

### Tagging

| Operation             | Status       | Notes                                                                                                                             | AWS Docs                                                                                                  |
| --------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `TagLogGroup`         | ✅ Supported | Adds tags to a log group; enforces AWS's key/value length, reserved `aws:` prefix and 50-tag limits before mutating               | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TagLogGroup.html)         |
| `UntagLogGroup`       | ✅ Supported | Removes tags from a log group                                                                                                     | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_UntagLogGroup.html)       |
| `ListTagsLogGroup`    | ✅ Supported | Returns tags for a log group                                                                                                      | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_ListTagsLogGroup.html)    |
| `TagResource`         | ✅ Supported | Modern, ARN-addressed sibling of TagLogGroup (#1195); resolves `resourceArn` to a log group and shares its validation and storage | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported | Modern, ARN-addressed sibling of UntagLogGroup (#1195)                                                                            | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported | Modern, ARN-addressed sibling of ListTagsLogGroup (#1195)                                                                         | [docs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_ListTagsForResource.html) |

## Related

- [CloudWatch Logs](../cloudwatch-logs.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
