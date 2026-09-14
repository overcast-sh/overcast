---
title: "CloudWatch Logs — Amazon CloudWatch Logs"
description: "Quick start, filter patterns, metric filters that feed CloudWatch alarms, retention enforcement, the input rules applied before anything is written, and the three operations that return 501."
section: "Service Reference"
tags:
  - cloudwatch
  - docs
  - logs
  - services
---

# CloudWatch Logs — Amazon CloudWatch Logs

Log groups, streams and events are stored and queryable, and retention is
enforced for real. Lambda writes its own output here, so `aws logs tail` works
the way it does on AWS.

**Status:** ⚠️ Partial

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
aws logs create-log-group --log-group-name /custom/demo
aws logs create-log-stream --log-group-name /custom/demo --log-stream-name run-1
aws logs put-log-events --log-group-name /custom/demo --log-stream-name run-1 \
  --log-events "timestamp=$(($(date +%s) * 1000)),message=hello"

aws logs filter-log-events --log-group-name /custom/demo
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

A Lambda function gets `/aws/lambda/<function-name>` created for it at
`CreateFunction`, and a stream per invocation. Its stdout and stderr land there.

## What works

| Area | Behaviour |
| --- | --- |
| Groups, streams, events | Full CRUD, plus `GetLogEvents` and `FilterLogEvents` |
| Paging | `DescribeLogGroups` and `DescribeLogStreams` honour `limit` (default and maximum 50) and page with `nextToken` |
| Ingestion window | An out-of-window log event is discarded and reported in `rejectedLogEventsInfo`, and the call still succeeds |
| Filter patterns | Plain text, JSON (`{ $.field = value }`), and space-delimited (`[col, ...]`) patterns |
| Metric filters | `PutMetricFilter` turns matching log events — from `PutLogEvents` or a Lambda function's output — into CloudWatch datapoints |
| Retention | `PutRetentionPolicy` is enforced by a background sweep every 5 minutes, in every storage mode. A group with no policy keeps events indefinitely |
| Stream cleanup | The same sweep removes a stream once its last event has aged out and nothing is left buffered. A stream that never received an event is never removed |
| Tagging | `CreateLogGroup` applies `tags` atomically — a rejected request creates nothing. `TagLogGroup` merges |
| Live tail | `StartLiveTail` over the JSON protocol, which the console's tail view uses |
| Storage | In SQLite-backed modes, events live in a dedicated indexed table, so appends and time-range reads stay fast regardless of stream size |
| Protocols | AWS JSON 1.1 (`X-Amz-Target: Logs_20140328.<Operation>`) and Smithy RPC v2 CBOR |

A metric filter's datapoint carries the transformation's `metricValue` — a
number, or a field of the event such as `$size` or `$.latency` — with its
`dimensions` and `unit`, at the event's own timestamp, so a
`GetMetricStatistics` query or an alarm on the metric sees it.
`TestMetricFilter` previews a pattern against sample messages without
touching any log group.

## Validation

Three input rules are enforced before anything is written, so a bad request
never half-applies.

| Input | Rule |
| --- | --- |
| `retentionInDays` | One of 1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653 |
| Tags | At most 50 per group, keys 1–128 characters, values 0–256, no key beginning `aws:` |
| `logEvents` | Ascending by `timestamp`. An unordered batch is refused whole |
| Metric filters | One `metricTransformations` entry, a numeric or `$field` `metricValue`, at most three `dimensions`, and at most 100 filters per log group |

All four return `InvalidParameterException` and leave existing state untouched.
A dimension must reference a field, which only a JSON or space-delimited
pattern has, and cannot be combined with a `defaultValue`; the 101st filter
on a group is refused with `LimitExceededException`.
`AWS::Logs::LogGroup` inherits them from the service, so a template carrying an
unsupported `RetentionInDays` fails the resource and rolls the stack back.

An event's timestamp is the exception to that rule. One more than two hours
ahead of now, older than 14 days, or older than the log group's retention
period is discarded rather than refused: `PutLogEvents` still answers `200` and
names the discarded range in `rejectedLogEventsInfo`, which is the only signal
a client gets that the data never landed.

## Differences from AWS

| Area                      | On AWS                             | Overcast                                                                                                                                                                           |
| ------------------------- | ---------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Logs Insights             | `StartQuery` and `GetQueryResults` | Both return `501`, so `GetLogRecord` resolves only an `eventId` handed out by `FilterLogEvents`                                                                                    |
| Subscription filters      | Fan out to Lambda or Kinesis       | `PutSubscriptionFilter` returns `501`; there is no fan-out                                                                                                                         |
| Metric filters            | Publish whenever a log event matches | Publish only while service metrics are collected (the default). With `OVERCAST_SERVICE_METRICS=disabled` a filter is stored, described and tested but publishes nothing            |
| `defaultValue`            | Reported for a period with ingested but unmatched events | Decided per ingested batch: a minute any event of the batch matched gets no default from it                                                              |
| `StartLiveTail` over CBOR | Supported                          | Returns `501`; only the JSON protocol serves it                                                                                                                                    |
| Write timing              | No documented write buffer         | Events are buffered per stream for about 50 ms — flushed early on a burst, and synchronously on graceful shutdown — so a read immediately after a write may not see the last event |

## Gotchas

> [!WARNING]
> `StartLiveTail` carries a `streaming-` host prefix. The AWS SDK for Go v2
> prepends it to whatever host you configure — `streaming-localhost:4566`,
> which resolves nowhere — so a `BaseEndpoint` alone is not enough.

Pin the endpoint as immutable, which is the one resolver switch that suppresses
the prefix. Only the legacy resolver has it:

```go
client := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
    o.EndpointResolver = cloudwatchlogs.EndpointResolverFromURL(
        "http://localhost:4566",
        func(e *aws.Endpoint) { e.HostnameImmutable = true },
    )
})
```

Every other operation works from `o.BaseEndpoint` as usual — see
[Using AWS SDKs and CLI](../sdk-cli.md#go-aws-sdk-v2). The JavaScript SDK,
which the console's tail view uses, needs nothing extra.

<!-- BEGIN overcast:capabilities -->

## Operations

23 of 26 listed operations are implemented.
Per-operation status, notes and AWS API links: [CloudWatch Logs operations](cloudwatch-logs/operations.md).

<!-- END overcast:capabilities -->

## Related

- [CloudWatch](./cloudwatch.md) — metrics and alarms
- [Lambda](./lambda.md) — function logging configuration
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/Welcome.html)
